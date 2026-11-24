// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

const fakeAPIKey = "tk_unit_test_key"

// fakeTemps is an in-memory Temps API covering the endpoints the provider
// uses. It mimics the behaviour that matters to the provider: masked
// secrets, problem+json errors, 404s, async service provisioning, and the
// production environment created with every project.
type fakeTemps struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex
	seq int64

	projects     map[int64]*client.Project
	envs         map[int64]*client.Environment
	vars         map[int64]*fakeVar
	services     map[int64]*fakeService
	links        map[int64]*client.ServiceLink
	sources      map[int64]*fakeSource
	backups      map[string]int64 // backup uuid -> source id
	schedules    map[int64]*fakeSchedule
	monitors     map[int64]*client.Monitor
	pages        map[int64]*fakePage
	domains      map[int64]*fakeDomain
	failures     map[string]fakeFailure // "METHOD /path" -> injected error
	noPagePasswd bool                   // simulate a server without the password endpoint
	requests     []string

	// s3Creating counts S3 source creations in flight. Like Temps, two that
	// overlap while no default exists race for it and one fails with a 500.
	s3Creating atomic.Int32
}

type fakeFailure struct {
	status int
	body   string
}

type fakeVar struct {
	projectID int64
	v         client.EnvironmentVariable
	value     string
}

type fakeService struct {
	s      client.Service
	params map[string]any
	gets   int
}

type fakeSource struct {
	s         client.S3Source
	accessKey string
	secretKey string
	useTLS    *bool
}

type fakeSchedule struct {
	s          client.BackupSchedule
	serviceIDs []int64
}

type fakePage struct {
	p        client.StatusPage
	password string
	subs     client.StatusPageSubscriptions
}

type fakeDomain struct {
	pageID int64
	d      client.StatusPageDomain
}

func newFakeTemps(t *testing.T) *fakeTemps {
	t.Helper()
	f := &fakeTemps{
		t: t, projects: map[int64]*client.Project{}, envs: map[int64]*client.Environment{},
		vars: map[int64]*fakeVar{}, services: map[int64]*fakeService{}, links: map[int64]*client.ServiceLink{},
		sources: map[int64]*fakeSource{}, backups: map[string]int64{}, schedules: map[int64]*fakeSchedule{},
		monitors: map[int64]*client.Monitor{}, pages: map[int64]*fakePage{}, domains: map[int64]*fakeDomain{},
		failures: map[string]fakeFailure{},
	}
	f.srv = httptest.NewServer(f.routes())
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTemps) nextID() int64 { f.seq++; return f.seq }

// fail makes the next request to "METHOD /api/path" return status and a problem body.
func (f *fakeTemps) fail(methodPath string, status int, title, detail string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := json.Marshal(map[string]string{"title": title, "detail": detail})
	f.failures[methodPath] = fakeFailure{status: status, body: string(b)}
}

// failRaw makes the next request to "METHOD /api/path" return status and body verbatim.
func (f *fakeTemps) failRaw(methodPath string, status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[methodPath] = fakeFailure{status: status, body: body}
}

func (f *fakeTemps) with(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func problem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"title": title, "detail": detail})
}

func notFound(w http.ResponseWriter, what string, id any) {
	problem(w, http.StatusNotFound, "Resource Not Found", fmt.Sprintf("%s %v not found", what, id))
}

func pathID(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id
}

func decode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }

func (f *fakeTemps) routes() http.Handler {
	mux := http.NewServeMux()
	// "/api/projects/by-slug/{slug}" is ambiguous with "/api/projects/{id}/..."
	// for ServeMux, so it lives on its own mux.
	slugMux := http.NewServeMux()
	h := func(pattern string, fn func(http.ResponseWriter, *http.Request)) {
		target := mux
		if strings.Contains(pattern, "/by-slug/") {
			target = slugMux
		}
		raceWindow := pattern == "POST /api/backups/s3-sources"
		target.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if raceWindow {
				f.s3Creating.Add(1)
				defer f.s3Creating.Add(-1)
				time.Sleep(50 * time.Millisecond)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			f.requests = append(f.requests, r.Method+" "+r.URL.Path)
			if r.Header.Get("Authorization") != "Bearer "+fakeAPIKey {
				problem(w, http.StatusUnauthorized, "Unauthorized", "missing or invalid API key")
				return
			}
			if fl, ok := f.failures[r.Method+" "+r.URL.Path]; ok {
				delete(f.failures, r.Method+" "+r.URL.Path)
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(fl.status)
				_, _ = w.Write([]byte(fl.body))
				return
			}
			fn(w, r)
		})
	}

	// ---------------------------------------------------------------- projects
	h("POST /api/projects", func(w http.ResponseWriter, r *http.Request) {
		var req client.CreateProjectRequest
		if err := decode(r, &req); err != nil {
			problem(w, 422, "Unprocessable", err.Error())
			return
		}
		st := "git"
		if req.SourceType != nil {
			st = *req.SourceType
		}
		if st == "git" && (req.RepoOwner == nil || req.RepoName == nil) {
			problem(w, 400, "Missing Repository Information", "For Git-based projects, both repo_name and repo_owner must be specified.")
			return
		}
		for _, p := range f.projects {
			if strings.EqualFold(p.Name, req.Name) {
				problem(w, 409, "Conflict", "project name "+req.Name+" is already in use")
				return
			}
		}
		slug := strings.ToLower(req.Name)
		if req.ExpectedSlug != nil {
			slug = *req.ExpectedSlug
		}
		p := &client.Project{
			ID: f.nextID(), Slug: slug, Name: req.Name, SourceType: st, GitURL: req.GitURL,
			RepoOwner: req.RepoOwner, RepoName: req.RepoName, GitProviderConnectionID: req.GitProviderConnectionID,
			IsPublicRepo: req.IsPublicRepo != nil && *req.IsPublicRepo, Directory: ".", Preset: req.Preset, ProjectType: "server",
		}
		if req.MainBranch != nil {
			p.MainBranch = *req.MainBranch
		}
		if req.Directory != nil {
			p.Directory = *req.Directory
		}
		if req.Preset != nil {
			pc := client.PresetConfig{Preset: *req.Preset}
			if req.PresetConfig != nil {
				pc = *req.PresetConfig
				pc.Preset = *req.Preset
			}
			p.PresetConfig = &pc
		}
		ad := req.AutomaticDeploy != nil && *req.AutomaticDeploy
		p.DeploymentConfig = client.DeploymentConfig{ExposedPort: req.ExposedPort, AutomaticDeploy: &ad}
		f.projects[p.ID] = p
		env := &client.Environment{ID: f.nextID(), ProjectID: p.ID, Name: "production", Slug: "production",
			Branch: ptr(p.MainBranch), MainURL: "http://" + slug + "-production.example.com", Subdomain: slug + "-production"}
		f.envs[env.ID] = env
		writeJSON(w, 200, p)
	})
	h("GET /api/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		if p, ok := f.projects[pathID(r, "id")]; ok {
			writeJSON(w, 200, p)
			return
		}
		notFound(w, "project", r.PathValue("id"))
	})
	h("GET /api/projects/by-slug/{slug}", func(w http.ResponseWriter, r *http.Request) {
		for _, p := range f.projects {
			if p.Slug == r.PathValue("slug") {
				writeJSON(w, 200, p)
				return
			}
		}
		notFound(w, "project", r.PathValue("slug"))
	})
	h("POST /api/projects/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
		p, ok := f.projects[pathID(r, "id")]
		if !ok {
			notFound(w, "project", r.PathValue("id"))
			return
		}
		var req client.UpdateProjectSettingsRequest
		_ = decode(r, &req)
		set := func(dst *string, v *string) {
			if v != nil {
				*dst = *v
			}
		}
		set(&p.Name, req.Name)
		set(&p.Slug, req.Slug)
		set(&p.MainBranch, req.MainBranch)
		set(&p.Directory, req.Directory)
		if req.RepoOwner != nil {
			p.RepoOwner = req.RepoOwner
		}
		if req.RepoName != nil {
			p.RepoName = req.RepoName
		}
		if req.GitProviderConnectionID != nil {
			p.GitProviderConnectionID = req.GitProviderConnectionID
		}
		if req.EnablePreviewEnvironments != nil {
			p.EnablePreviewEnvironments = *req.EnablePreviewEnvironments
		}
		if req.Preset != nil {
			p.Preset = req.Preset
			pc := client.PresetConfig{Preset: *req.Preset}
			if req.PresetConfig != nil { // replaced wholesale, like the real server
				pc = *req.PresetConfig
				pc.Preset = *req.Preset
			}
			p.PresetConfig = &pc
		}
		writeJSON(w, 200, p)
	})
	h("PATCH /api/projects/{id}/source", func(w http.ResponseWriter, r *http.Request) {
		p, ok := f.projects[pathID(r, "id")]
		if !ok {
			notFound(w, "project", r.PathValue("id"))
			return
		}
		var req map[string]string
		_ = decode(r, &req)
		p.SourceType = req["source_type"]
		writeJSON(w, 200, p)
	})
	h("POST /api/projects/{id}/automatic-deploy", func(w http.ResponseWriter, r *http.Request) {
		p, ok := f.projects[pathID(r, "id")]
		if !ok {
			notFound(w, "project", r.PathValue("id"))
			return
		}
		var req map[string]bool
		_ = decode(r, &req)
		v := req["automatic_deploy"]
		p.DeploymentConfig.AutomaticDeploy = &v
		writeJSON(w, 200, p)
	})
	h("PATCH /api/projects/{id}/deployment-config", func(w http.ResponseWriter, r *http.Request) {
		p, ok := f.projects[pathID(r, "id")]
		if !ok {
			notFound(w, "project", r.PathValue("id"))
			return
		}
		var req map[string]int64
		_ = decode(r, &req)
		v := req["exposedPort"]
		p.DeploymentConfig.ExposedPort = &v
		writeJSON(w, 200, p)
	})
	h("DELETE /api/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r, "id")
		if _, ok := f.projects[id]; !ok {
			notFound(w, "project", id)
			return
		}
		delete(f.projects, id)
		for eid, e := range f.envs {
			if e.ProjectID == id {
				delete(f.envs, eid)
			}
		}
		for vid, v := range f.vars {
			if v.projectID == id {
				delete(f.vars, vid)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// ------------------------------------------------------------ environments
	h("GET /api/projects/{pid}/environments", func(w http.ResponseWriter, r *http.Request) {
		out := []client.Environment{}
		for _, e := range f.envs {
			if e.ProjectID == pathID(r, "pid") {
				out = append(out, *e)
			}
		}
		slices.SortFunc(out, func(a, b client.Environment) int { return int(a.ID - b.ID) })
		writeJSON(w, 200, out)
	})
	h("POST /api/projects/{pid}/environments", func(w http.ResponseWriter, r *http.Request) {
		pid := pathID(r, "pid")
		p, ok := f.projects[pid]
		if !ok {
			notFound(w, "project", pid)
			return
		}
		var req client.CreateEnvironmentRequest
		_ = decode(r, &req)
		e := &client.Environment{ID: f.nextID(), ProjectID: pid, Name: req.Name, Slug: req.Name, Branch: ptr(req.Branch),
			IsPreview: req.SetAsPreview, MainURL: "http://" + p.Slug + "-" + req.Name + ".example.com", Subdomain: p.Slug + "-" + req.Name}
		f.envs[e.ID] = e
		writeJSON(w, 201, e)
	})
	envLookup := func(w http.ResponseWriter, r *http.Request) *client.Environment {
		e, ok := f.envs[pathID(r, "id")]
		if !ok || e.ProjectID != pathID(r, "pid") {
			notFound(w, "environment", r.PathValue("id"))
			return nil
		}
		return e
	}
	h("GET /api/projects/{pid}/environments/{id}", func(w http.ResponseWriter, r *http.Request) {
		if e := envLookup(w, r); e != nil {
			writeJSON(w, 200, e)
		}
	})
	h("PUT /api/projects/{pid}/environments/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
		e := envLookup(w, r)
		if e == nil {
			return
		}
		var req client.UpdateEnvironmentSettingsRequest
		_ = decode(r, &req)
		if req.Branch != nil {
			e.Branch = req.Branch
		}
		if req.Protected != nil {
			e.Protected = *req.Protected
		}
		if req.ForceHTTPS != nil {
			e.ForceHTTPS = req.ForceHTTPS
		}
		writeJSON(w, 200, e)
	})
	h("DELETE /api/projects/{pid}/environments/{id}", func(w http.ResponseWriter, r *http.Request) {
		if e := envLookup(w, r); e != nil {
			delete(f.envs, e.ID)
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// --------------------------------------------------------------- variables
	toVar := func(fv *fakeVar) client.EnvironmentVariable { return fv.v }
	h("GET /api/projects/{pid}/env-vars", func(w http.ResponseWriter, r *http.Request) {
		out := []client.EnvironmentVariable{}
		for _, fv := range f.vars {
			if fv.projectID == pathID(r, "pid") {
				v := toVar(fv)
				if v.IsSecret {
					v.Value = nil
				} else {
					v.Value = ptr(client.MaskedValue) // the list masks every value
				}
				out = append(out, v)
			}
		}
		writeJSON(w, 200, out)
	})
	saveVar := func(w http.ResponseWriter, r *http.Request, fv *fakeVar, status int) {
		var req client.EnvironmentVariableRequest
		_ = decode(r, &req)
		if req.Value == nil && fv.v.ID == 0 {
			problem(w, 422, "Unprocessable", "value is required")
			return
		}
		if fv.v.IsSecret && req.IsSecret != nil && !*req.IsSecret {
			problem(w, 400, "Validation Error", "a secret variable cannot be made non-secret")
			return
		}
		if fv.v.ID == 0 {
			fv.v.ID = f.nextID()
		}
		fv.v.Key = req.Key
		if req.Value != nil {
			fv.value = *req.Value
		}
		if req.IsSecret != nil {
			fv.v.IsSecret = *req.IsSecret
		}
		fv.v.IncludeInPreview = req.IncludeInPreview
		fv.v.Environments = nil
		for _, id := range req.EnvironmentIDs {
			e, ok := f.envs[id]
			if !ok {
				problem(w, 400, "Validation Error", fmt.Sprintf("environment %d does not exist", id))
				return
			}
			fv.v.Environments = append(fv.v.Environments, client.EnvironmentRef{ID: id, Name: e.Name})
		}
		f.vars[fv.v.ID] = fv
		out := fv.v
		if !out.IsSecret {
			out.Value = ptr(fv.value)
		}
		writeJSON(w, status, out)
	}
	h("POST /api/projects/{pid}/env-vars", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := f.projects[pathID(r, "pid")]; !ok {
			notFound(w, "project", r.PathValue("pid"))
			return
		}
		saveVar(w, r, &fakeVar{projectID: pathID(r, "pid")}, 201)
	})
	h("PUT /api/projects/{pid}/env-vars/{id}", func(w http.ResponseWriter, r *http.Request) {
		fv, ok := f.vars[pathID(r, "id")]
		if !ok || fv.projectID != pathID(r, "pid") {
			notFound(w, "environment variable", r.PathValue("id"))
			return
		}
		saveVar(w, r, fv, 200)
	})
	h("DELETE /api/projects/{pid}/env-vars/{id}", func(w http.ResponseWriter, r *http.Request) {
		fv, ok := f.vars[pathID(r, "id")]
		if !ok || fv.projectID != pathID(r, "pid") {
			notFound(w, "environment variable", r.PathValue("id"))
			return
		}
		delete(f.vars, fv.v.ID)
		w.WriteHeader(http.StatusNoContent)
	})
	h("GET /api/projects/{pid}/env-vars/{key}/value", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("var_id"), 10, 64)
		fv, ok := f.vars[id]
		if !ok || fv.v.Key != r.PathValue("key") {
			notFound(w, "environment variable", r.PathValue("key"))
			return
		}
		if fv.v.IsSecret {
			problem(w, 403, "Secret environment variable is write-only", "Secret environment variable '"+fv.v.Key+"' is write-only and cannot be revealed")
			return
		}
		writeJSON(w, 200, map[string]string{"value": fv.value})
	})

	// ---------------------------------------------------------------- services
	h("GET /api/external-services/types", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, []string{"postgres", "redis", "s3"})
	})
	h("GET /api/external-services/types/{type}/parameters", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"properties": map[string]any{
			"database": map[string]any{"type": "string", "x-editable": false}, "username": map[string]any{"type": "string", "x-editable": false},
			"password": map[string]any{"type": []string{"string", "null"}}, "max_connections": map[string]any{"type": "integer"},
			"port": map[string]any{"type": []string{"string", "null"}}, "docker_image": map[string]any{"type": []string{"string", "null"}},
		}})
	})
	h("POST /api/external-services", func(w http.ResponseWriter, r *http.Request) {
		var req client.CreateServiceRequest
		_ = decode(r, &req)
		if _, ok := req.Parameters["database"]; !ok && req.ServiceType == "postgres" {
			problem(w, 400, "Bad Request", "Parameter validation failed for service 0: 'database' is required for PostgreSQL")
			return
		}
		if n, ok := req.Parameters["max_connections"]; ok {
			if _, isNum := n.(float64); !isNum {
				problem(w, 400, "Invalid parameters", "max_connections must be an integer")
				return
			}
		}
		topo := "standalone"
		if req.Topology != nil {
			topo = *req.Topology
		}
		params := map[string]any{"docker_image": "postgres:18", "port": "5432"}
		for k, v := range req.Parameters {
			params[k] = v
		}
		if _, ok := params["password"]; !ok {
			params["password"] = "generated-secret"
		}
		s := &fakeService{s: client.Service{ID: f.nextID(), Name: req.Name, ServiceType: req.ServiceType, Status: "pending", Topology: topo, Version: req.Version}, params: params}
		f.services[s.s.ID] = s
		writeJSON(w, 201, s.s)
	})
	svcDetails := func(s *fakeService) client.ServiceDetails {
		cur := map[string]any{}
		for k, v := range s.params {
			cur[k] = v
		}
		cur["password"] = client.MaskedValue
		return client.ServiceDetails{Service: s.s, CurrentParameters: cur, SensitiveParameters: []string{"password"}}
	}
	h("GET /api/external-services", func(w http.ResponseWriter, r *http.Request) {
		out := []client.Service{}
		for _, s := range f.services {
			out = append(out, s.s)
		}
		writeJSON(w, 200, out)
	})
	h("GET /api/external-services/{id}", func(w http.ResponseWriter, r *http.Request) {
		s, ok := f.services[pathID(r, "id")]
		if !ok {
			notFound(w, "service", r.PathValue("id"))
			return
		}
		s.gets++
		switch {
		case strings.HasSuffix(s.s.Name, "-stuck"): // never becomes ready
		case strings.HasSuffix(s.s.Name, "-broken") && s.gets >= 2:
			s.s.Status, s.s.ErrorMessage = "failed", ptr("container exited with code 1")
		case s.s.Status == "pending" && s.gets >= 2: // asynchronous provisioning
			s.s.Status = "running"
		}
		writeJSON(w, 200, svcDetails(s))
	})
	h("PUT /api/external-services/{id}", func(w http.ResponseWriter, r *http.Request) {
		s, ok := f.services[pathID(r, "id")]
		if !ok {
			notFound(w, "service", r.PathValue("id"))
			return
		}
		var req struct {
			Parameters map[string]any `json:"parameters"`
		}
		_ = decode(r, &req)
		for k := range req.Parameters {
			if k == "database" || k == "username" || k == "password" || k == "host" {
				problem(w, 400, "Bad Request", fmt.Sprintf("Parameter validation failed for service %d: Cannot update parameter '%s' for PostgreSQL.", s.s.ID, k))
				return
			}
		}
		for k, v := range req.Parameters {
			s.params[k] = v
		}
		writeJSON(w, 200, s.s)
	})
	h("POST /api/external-services/{id}/upgrade", func(w http.ResponseWriter, r *http.Request) {
		s, ok := f.services[pathID(r, "id")]
		if !ok {
			notFound(w, "service", r.PathValue("id"))
			return
		}
		var req map[string]string
		_ = decode(r, &req)
		s.params["docker_image"] = req["docker_image"]
		s.s.Status, s.gets = "pending", 0
		writeJSON(w, 200, s.s)
	})
	h("DELETE /api/external-services/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r, "id")
		if _, ok := f.services[id]; !ok {
			notFound(w, "service", id)
			return
		}
		delete(f.services, id)
		for lid, l := range f.links {
			if l.Service.ID == id {
				delete(f.links, lid)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h("POST /api/external-services/{id}/projects", func(w http.ResponseWriter, r *http.Request) {
		s, ok := f.services[pathID(r, "id")]
		if !ok {
			notFound(w, "service", r.PathValue("id"))
			return
		}
		var req client.LinkServiceRequest
		_ = decode(r, &req)
		p, ok := f.projects[req.ProjectID]
		if !ok {
			notFound(w, "project", req.ProjectID)
			return
		}
		mode := "project"
		if req.DatabaseProvisioningMode != nil {
			mode = *req.DatabaseProvisioningMode
		}
		l := &client.ServiceLink{ID: f.nextID(), DatabaseProvisioningMode: mode, CustomDatabaseName: req.CustomDatabaseName, Service: s.s}
		l.Project.ID, l.Project.Slug = p.ID, p.Slug
		f.links[l.ID] = l
		writeJSON(w, 201, l)
	})
	h("GET /api/external-services/{id}/projects", func(w http.ResponseWriter, r *http.Request) {
		out := []client.ServiceLink{}
		for _, l := range f.links {
			if l.Service.ID == pathID(r, "id") {
				out = append(out, *l)
			}
		}
		writeJSON(w, 200, out)
	})
	linkLookup := func(w http.ResponseWriter, r *http.Request) *client.ServiceLink {
		for _, l := range f.links {
			if l.Service.ID == pathID(r, "id") && l.Project.ID == pathID(r, "pid") {
				return l
			}
		}
		notFound(w, "service link", r.PathValue("id")+"/"+r.PathValue("pid"))
		return nil
	}
	h("DELETE /api/external-services/{id}/projects/{pid}", func(w http.ResponseWriter, r *http.Request) {
		if l := linkLookup(w, r); l != nil {
			delete(f.links, l.ID)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	h("GET /api/external-services/{id}/projects/{pid}/environment", func(w http.ResponseWriter, r *http.Request) {
		if linkLookup(w, r) != nil {
			writeJSON(w, 200, map[string]string{"POSTGRES_HOST": "***", "POSTGRES_PASSWORD": "***"})
		}
	})
	h("GET /api/external-services/{id}/projects/{pid}/environment/{name}", func(w http.ResponseWriter, r *http.Request) {
		if linkLookup(w, r) == nil {
			return
		}
		vals := map[string]string{"POSTGRES_HOST": "db.internal", "POSTGRES_PASSWORD": "generated-secret"}
		v, ok := vals[r.PathValue("name")]
		if !ok {
			notFound(w, "variable", r.PathValue("name"))
			return
		}
		writeJSON(w, 200, client.ServiceVariable{Name: r.PathValue("name"), Value: v, Sensitive: true})
	})

	// ------------------------------------------------------------- s3 sources
	masked := func(s *fakeSource) client.S3Source {
		out := s.s
		out.AccessKeyID, out.SecretKey = client.MaskedValue, client.MaskedValue
		return out
	}
	h("POST /api/backups/s3-sources/test", func(w http.ResponseWriter, r *http.Request) {
		var req client.S3SourceRequest
		_ = decode(r, &req)
		ok := req.SecretKey != nil && *req.SecretKey != "wrong"
		writeJSON(w, 200, client.ConnectionTest{OK: ok, Message: map[bool]string{true: "ok", false: "Access Denied"}[ok]})
	})
	h("GET /api/backups/s3-sources", func(w http.ResponseWriter, r *http.Request) {
		out := []client.S3Source{}
		for _, s := range f.sources {
			out = append(out, masked(s))
		}
		writeJSON(w, 200, out)
	})
	h("POST /api/backups/s3-sources", func(w http.ResponseWriter, r *http.Request) {
		var req client.S3SourceRequest
		_ = decode(r, &req)
		s := &fakeSource{s: client.S3Source{ID: f.nextID(), Name: *req.Name, BucketName: *req.BucketName, BucketPath: *req.BucketPath,
			Region: *req.Region, Endpoint: req.Endpoint, ForcePathStyle: req.ForcePathStyle}, accessKey: *req.AccessKeyID, secretKey: *req.SecretKey, useTLS: req.UseTLS}
		hasDefault := false
		for _, o := range f.sources {
			hasDefault = hasDefault || o.s.IsDefault
		}
		if !hasDefault && f.s3Creating.Load() > 1 {
			problem(w, 500, "Internal Server Error", "The backup operation could not be completed")
			return
		}
		s.s.IsDefault = len(f.sources) == 0 || (req.IsDefault != nil && *req.IsDefault)
		if s.s.IsDefault {
			for _, o := range f.sources {
				o.s.IsDefault = false
			}
		}
		f.sources[s.s.ID] = s
		writeJSON(w, 201, masked(s))
	})
	srcLookup := func(w http.ResponseWriter, r *http.Request) *fakeSource {
		s, ok := f.sources[pathID(r, "id")]
		if !ok {
			notFound(w, "S3 source", r.PathValue("id"))
		}
		return s
	}
	h("GET /api/backups/s3-sources/{id}", func(w http.ResponseWriter, r *http.Request) {
		if s := srcLookup(w, r); s != nil {
			writeJSON(w, 200, masked(s))
		}
	})
	h("PATCH /api/backups/s3-sources/{id}", func(w http.ResponseWriter, r *http.Request) {
		s := srcLookup(w, r)
		if s == nil {
			return
		}
		var req client.S3SourceRequest
		_ = decode(r, &req)
		set := func(dst *string, v *string) {
			if v != nil {
				*dst = *v
			}
		}
		set(&s.s.Name, req.Name)
		set(&s.s.BucketName, req.BucketName)
		set(&s.s.BucketPath, req.BucketPath)
		set(&s.s.Region, req.Region)
		set(&s.accessKey, req.AccessKeyID)
		set(&s.secretKey, req.SecretKey)
		s.s.Endpoint = req.Endpoint
		if req.ForcePathStyle != nil {
			s.s.ForcePathStyle = req.ForcePathStyle
		}
		writeJSON(w, 200, masked(s))
	})
	h("POST /api/backups/s3-sources/{id}/set-default", func(w http.ResponseWriter, r *http.Request) {
		s := srcLookup(w, r)
		if s == nil {
			return
		}
		for _, o := range f.sources {
			o.s.IsDefault = false
		}
		s.s.IsDefault = true
		writeJSON(w, 200, masked(s))
	})
	h("GET /api/backups/s3-sources/{id}/backups", func(w http.ResponseWriter, r *http.Request) {
		if srcLookup(w, r) == nil {
			return
		}
		out := []client.BackupSummary{}
		for u, sid := range f.backups {
			if sid == pathID(r, "id") {
				out = append(out, client.BackupSummary{BackupID: u, State: "completed"})
			}
		}
		writeJSON(w, 200, map[string]any{"backups": out})
	})
	h("DELETE /api/backups/s3-sources/{id}", func(w http.ResponseWriter, r *http.Request) {
		s := srcLookup(w, r)
		if s == nil {
			return
		}
		n := 0
		for _, sid := range f.backups {
			if sid == s.s.ID {
				n++
			}
		}
		if n > 0 {
			problem(w, 400, "Validation Error", fmt.Sprintf("Cannot delete S3 source '%s': still referenced by %d backup record(s)", s.s.Name, n))
			return
		}
		if s.s.IsDefault && len(f.sources) > 1 {
			problem(w, 400, "Validation Error", fmt.Sprintf("S3 source '%s' is the default. Set a different source as default before deleting.", s.s.Name))
			return
		}
		delete(f.sources, s.s.ID)
		w.WriteHeader(http.StatusNoContent)
	})
	h("DELETE /api/backups/{uuid}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := f.backups[r.PathValue("uuid")]; !ok {
			notFound(w, "backup", r.PathValue("uuid"))
			return
		}
		delete(f.backups, r.PathValue("uuid"))
		w.WriteHeader(http.StatusNoContent)
	})

	// --------------------------------------------------------------- schedules
	h("POST /api/backups/schedules", func(w http.ResponseWriter, r *http.Request) {
		var req client.CreateBackupScheduleRequest
		_ = decode(r, &req)
		if len(strings.Fields(req.ScheduleExpression)) < 6 {
			problem(w, 400, "Validation Error", "Invalid backup schedule: "+req.ScheduleExpression)
			return
		}
		var src int64
		if req.S3SourceID != nil {
			src = *req.S3SourceID
		} else {
			for _, s := range f.sources {
				if s.s.IsDefault {
					src = s.s.ID
				}
			}
		}
		s := &fakeSchedule{s: client.BackupSchedule{ID: f.nextID(), Name: req.Name, Description: req.Description, BackupType: req.BackupType,
			RetentionPeriod: req.RetentionPeriod, S3SourceID: src, ScheduleExpression: req.ScheduleExpression, Enabled: req.Enabled,
			Tags: req.Tags, TargetAllServices: req.TargetAllServices != nil && *req.TargetAllServices,
			IncludeControlPlane: req.IncludeControlPlane != nil && *req.IncludeControlPlane, MaxRuntimeSecs: req.MaxRuntimeSecs}, serviceIDs: req.ServiceIDs}
		f.schedules[s.s.ID] = s
		writeJSON(w, 201, s.s)
	})
	schedLookup := func(w http.ResponseWriter, r *http.Request) *fakeSchedule {
		s, ok := f.schedules[pathID(r, "id")]
		if !ok {
			problem(w, 404, "Resource Not Found", "BackupSchedule not found: Backup schedule not found")
		}
		return s
	}
	h("GET /api/backups/schedules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if s := schedLookup(w, r); s != nil {
			writeJSON(w, 200, s.s)
		}
	})
	h("PATCH /api/backups/schedules/{id}", func(w http.ResponseWriter, r *http.Request) {
		s := schedLookup(w, r)
		if s == nil {
			return
		}
		var req client.UpdateBackupScheduleRequest
		_ = decode(r, &req)
		if req.Name != nil {
			s.s.Name = *req.Name
		}
		if req.Description != nil {
			s.s.Description = req.Description
		}
		if req.RetentionPeriod != nil {
			s.s.RetentionPeriod = *req.RetentionPeriod
		}
		if req.ScheduleExpression != nil {
			s.s.ScheduleExpression = *req.ScheduleExpression
		}
		if req.Enabled != nil {
			s.s.Enabled = *req.Enabled
		}
		if req.Tags != nil {
			s.s.Tags = *req.Tags
		}
		if req.ServiceIDs != nil {
			s.serviceIDs = *req.ServiceIDs
		}
		if req.TargetAllServices != nil {
			s.s.TargetAllServices = *req.TargetAllServices
		}
		if req.IncludeControlPlane != nil {
			s.s.IncludeControlPlane = *req.IncludeControlPlane
		}
		s.s.MaxRuntimeSecs = req.MaxRuntimeSecs
		writeJSON(w, 200, s.s)
	})
	h("GET /api/backups/schedules/{id}/services", func(w http.ResponseWriter, r *http.Request) {
		s := schedLookup(w, r)
		if s == nil {
			return
		}
		out := []map[string]any{}
		for _, id := range s.serviceIDs {
			out = append(out, map[string]any{"id": id, "name": "svc", "service_type": "postgres"})
		}
		writeJSON(w, 200, out)
	})
	h("PATCH /api/backups/schedules/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		if s := schedLookup(w, r); s != nil {
			s.s.Enabled = r.PathValue("action") == "enable"
			w.WriteHeader(http.StatusOK)
		}
	})
	h("DELETE /api/backups/schedules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if s := schedLookup(w, r); s != nil {
			delete(f.schedules, s.s.ID)
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// ---------------------------------------------------------------- monitors
	h("POST /api/projects/{pid}/monitors", func(w http.ResponseWriter, r *http.Request) {
		var req client.CreateMonitorRequest
		_ = decode(r, &req)
		e, ok := f.envs[req.EnvironmentID]
		if !ok || e.ProjectID != pathID(r, "pid") {
			problem(w, 400, "Validation Error", fmt.Sprintf("environment %d does not belong to project %s", req.EnvironmentID, r.PathValue("pid")))
			return
		}
		interval := int64(60)
		if req.CheckIntervalSeconds != nil {
			interval = max(*req.CheckIntervalSeconds, 30)
		}
		m := &client.Monitor{ID: f.nextID(), ProjectID: e.ProjectID, EnvironmentID: &e.ID, Name: req.Name, MonitorType: req.MonitorType,
			CheckPath: req.CheckPath, CheckIntervalSeconds: interval, MonitorURL: e.MainURL, IsActive: true}
		if m.CheckPath != nil {
			m.MonitorURL += *m.CheckPath
		}
		f.monitors[m.ID] = m
		writeJSON(w, 201, m)
	})
	h("GET /api/monitors/{id}", func(w http.ResponseWriter, r *http.Request) {
		if m, ok := f.monitors[pathID(r, "id")]; ok {
			writeJSON(w, 200, m)
			return
		}
		notFound(w, "monitor", r.PathValue("id"))
	})
	h("PATCH /api/monitors/{id}", func(w http.ResponseWriter, r *http.Request) {
		m, ok := f.monitors[pathID(r, "id")]
		if !ok {
			notFound(w, "monitor", r.PathValue("id"))
			return
		}
		var req map[string]string
		_ = decode(r, &req)
		p := req["check_path"]
		m.CheckPath = &p
		writeJSON(w, 200, m)
	})
	h("DELETE /api/monitors/{id}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := f.monitors[pathID(r, "id")]; !ok {
			notFound(w, "monitor", r.PathValue("id"))
			return
		}
		delete(f.monitors, pathID(r, "id"))
		w.WriteHeader(http.StatusNoContent)
	})

	// ------------------------------------------------------------ status pages
	applyPage := func(p *fakePage, req client.StatusPageRequest) error {
		if req.Title != nil {
			p.p.Title = *req.Title
		}
		if req.Description != nil {
			p.p.Description = req.Description
		}
		if req.Monitors != nil {
			if len(*req.Monitors) == 0 {
				return fmt.Errorf("a status page needs at least one component; select a monitor to show")
			}
			p.p.Monitors = nil
			for i, m := range *req.Monitors {
				if _, ok := f.monitors[m.MonitorID]; !ok {
					return fmt.Errorf("monitor %d not found", m.MonitorID)
				}
				m.Position = int64(i)
				p.p.Monitors = append(p.p.Monitors, m)
			}
		}
		if req.Appearance != nil {
			a := *req.Appearance
			if a.Theme == nil {
				a.Theme = ptr("auto")
			}
			if a.ShowUptimePercentage == nil {
				a.ShowUptimePercentage = ptr(true)
			}
			if a.ShowResponseTimes == nil {
				a.ShowResponseTimes = ptr(true)
			}
			p.p.Appearance = a
		}
		return nil
	}
	h("POST /api/status-pages", func(w http.ResponseWriter, r *http.Request) {
		var req client.StatusPageRequest
		_ = decode(r, &req)
		p := &fakePage{p: client.StatusPage{ID: f.nextID(), Slug: fmt.Sprintf("%032d", f.seq),
			Appearance: client.StatusPageAppearance{Theme: ptr("auto"), ShowUptimePercentage: ptr(true), ShowResponseTimes: ptr(true)}}}
		if err := applyPage(p, req); err != nil {
			problem(w, 400, "Validation Error", err.Error())
			return
		}
		f.pages[p.p.ID] = p
		writeJSON(w, 201, p.p)
	})
	pageLookup := func(w http.ResponseWriter, r *http.Request) *fakePage {
		p, ok := f.pages[pathID(r, "id")]
		if !ok {
			notFound(w, "status page", r.PathValue("id"))
		}
		return p
	}
	h("GET /api/status-pages/{id}", func(w http.ResponseWriter, r *http.Request) {
		if p := pageLookup(w, r); p != nil {
			writeJSON(w, 200, p.p)
		}
	})
	h("PATCH /api/status-pages/{id}", func(w http.ResponseWriter, r *http.Request) {
		p := pageLookup(w, r)
		if p == nil {
			return
		}
		var req client.StatusPageRequest
		_ = decode(r, &req)
		if err := applyPage(p, req); err != nil {
			problem(w, 400, "Validation Error", err.Error())
			return
		}
		writeJSON(w, 200, p.p)
	})
	h("POST /api/status-pages/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		p := pageLookup(w, r)
		if p == nil {
			return
		}
		switch r.PathValue("action") {
		case "publish":
			p.p.IsPublished = true
		case "unpublish":
			p.p.IsPublished = false
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, 200, p.p)
	})
	h("PUT /api/status-pages/{id}/password", func(w http.ResponseWriter, r *http.Request) {
		if f.noPagePasswd {
			w.WriteHeader(http.StatusNotFound) // unknown route on older servers: no body
			return
		}
		p := pageLookup(w, r)
		if p == nil {
			return
		}
		var req map[string]*string
		_ = decode(r, &req)
		p.password = ""
		if req["password"] != nil {
			p.password = *req["password"]
		}
		writeJSON(w, 200, map[string]any{"has_password": p.password != "", "status_page_id": p.p.ID})
	})
	h("GET /api/status-pages/{id}/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		if p := pageLookup(w, r); p != nil {
			writeJSON(w, 200, p.subs)
		}
	})
	h("PUT /api/status-pages/{id}/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		p := pageLookup(w, r)
		if p == nil {
			return
		}
		_ = decode(r, &p.subs)
		writeJSON(w, 200, p.subs)
	})
	h("DELETE /api/status-pages/{id}", func(w http.ResponseWriter, r *http.Request) {
		if p := pageLookup(w, r); p != nil {
			delete(f.pages, p.p.ID)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	h("POST /api/status-pages/{id}/domains", func(w http.ResponseWriter, r *http.Request) {
		if pageLookup(w, r) == nil {
			return
		}
		var req struct {
			Hostname           string  `json:"hostname"`
			VerificationMethod *string `json:"verification_method"`
		}
		_ = decode(r, &req)
		m := "http-01"
		if req.VerificationMethod != nil {
			m = *req.VerificationMethod
		}
		d := &fakeDomain{pageID: pathID(r, "id"), d: client.StatusPageDomain{ID: f.nextID(), Hostname: req.Hostname, VerificationMethod: m, Status: "pending",
			DNSRecords: []client.DNSRecord{{RecordType: "CNAME", Name: req.Hostname, Value: "status.temps.example.com", Purpose: "routing"}}}}
		f.domains[d.d.ID] = d
		writeJSON(w, 201, d.d)
	})
	h("GET /api/status-pages/{id}/domains", func(w http.ResponseWriter, r *http.Request) {
		out := []client.StatusPageDomain{}
		for _, d := range f.domains {
			if d.pageID == pathID(r, "id") {
				out = append(out, d.d)
			}
		}
		writeJSON(w, 200, out)
	})
	h("POST /api/status-pages/{id}/domains/{did}/verify", func(w http.ResponseWriter, r *http.Request) {
		d, ok := f.domains[pathID(r, "did")]
		if !ok {
			notFound(w, "domain", r.PathValue("did"))
			return
		}
		d.d.Status, d.d.LastError = "unverified", ptr("CNAME record not found")
		writeJSON(w, 200, d.d)
	})
	h("DELETE /api/status-pages/{id}/domains/{did}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := f.domains[pathID(r, "did")]; !ok {
			notFound(w, "domain", r.PathValue("did"))
			return
		}
		delete(f.domains, pathID(r, "did"))
		w.WriteHeader(http.StatusNoContent)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/projects/by-slug/") {
			slugMux.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
