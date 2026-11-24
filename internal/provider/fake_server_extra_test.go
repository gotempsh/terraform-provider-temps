// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

// fakeExtra holds the fake's state for custom domains, webhooks, flags, IP
// access, cron monitors, deployment tokens and teams. Each handler mimics the
// real server's observable quirks that the provider has to cope with.
type fakeExtra struct {
	customDomains map[int64]*client.CustomDomain
	webhooks      map[int64]*fakeWebhook
	flags         map[string]*client.FeatureFlag // "<project>/<key>"
	ipAccess      map[string]*client.IPAccessPolicy
	crons         map[int64]*client.CronMonitor
	tokens        map[int64]*client.DeploymentToken
	teams         map[int64]*client.Team
	access        map[string]*client.ProjectAccess // "<project>/<team>"
	// callerIP is the address IP access lockout checks compare against.
	callerIP string
}

type fakeWebhook struct {
	w      client.Webhook
	secret string
}

func newFakeExtra() *fakeExtra {
	return &fakeExtra{
		customDomains: map[int64]*client.CustomDomain{}, webhooks: map[int64]*fakeWebhook{}, flags: map[string]*client.FeatureFlag{},
		ipAccess: map[string]*client.IPAccessPolicy{}, crons: map[int64]*client.CronMonitor{}, tokens: map[int64]*client.DeploymentToken{},
		teams: map[int64]*client.Team{}, access: map[string]*client.ProjectAccess{}, callerIP: "127.0.0.1",
	}
}

// envOf reports whether env belongs to project pid.
func (f *fakeTemps) envOf(pid, env int64) bool {
	e, ok := f.envs[env]
	return ok && e.ProjectID == pid
}

func (f *fakeTemps) extraRoutes(h func(string, func(http.ResponseWriter, *http.Request))) {
	x := f.x
	project := func(w http.ResponseWriter, r *http.Request) (int64, bool) {
		pid := pathID(r, "pid")
		if _, ok := f.projects[pid]; !ok {
			notFound(w, "project", pid)
			return 0, false
		}
		return pid, true
	}

	// ------------------------------------------------------ custom domains
	h("POST /api/projects/{pid}/custom-domains", func(w http.ResponseWriter, r *http.Request) {
		pid, ok := project(w, r)
		if !ok {
			return
		}
		var req client.CustomDomainRequest
		_ = decode(r, &req)
		name := strings.TrimSuffix(strings.ToLower(*req.Domain), ".")
		for _, d := range x.customDomains {
			if d.Domain == name {
				problem(w, 409, "Duplicate Domain", "Domain "+name+" already exists")
				return
			}
		}
		if !f.envOf(pid, *req.EnvironmentID) {
			problem(w, 400, "Invalid Domain", fmt.Sprintf("Environment %d does not belong to project %d", *req.EnvironmentID, pid))
			return
		}
		d := &client.CustomDomain{ID: f.nextID(), ProjectID: pid, Domain: name, RedirectTo: req.RedirectTo, StatusCode: req.StatusCode,
			Branch: req.Branch, ServiceName: req.ServiceName, Status: "pending"}
		d.Environment = &struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		}{ID: *req.EnvironmentID, Name: "production", Slug: "production"}
		x.customDomains[d.ID] = d
		writeJSON(w, 201, d)
	})
	domain := func(w http.ResponseWriter, r *http.Request) *client.CustomDomain {
		d, ok := x.customDomains[pathID(r, "id")]
		if !ok || d.ProjectID != pathID(r, "pid") {
			problem(w, 404, "Custom domain not found", "Domain does not belong to the specified project")
			return nil
		}
		return d
	}
	h("GET /api/projects/{pid}/custom-domains/{id}", func(w http.ResponseWriter, r *http.Request) {
		if d := domain(w, r); d != nil {
			writeJSON(w, 200, d)
		}
	})
	h("PUT /api/projects/{pid}/custom-domains/{id}", func(w http.ResponseWriter, r *http.Request) {
		d := domain(w, r)
		if d == nil {
			return
		}
		var req client.CustomDomainRequest
		_ = decode(r, &req)
		if req.Domain != nil {
			d.Domain = strings.TrimSuffix(strings.ToLower(*req.Domain), ".")
		}
		if req.EnvironmentID != nil {
			d.Environment.ID = *req.EnvironmentID
		}
		clear := func(dst **string, v *string) { // null keeps, "" clears
			if v != nil {
				if *v == "" {
					*dst = nil
				} else {
					*dst = v
				}
			}
		}
		clear(&d.RedirectTo, req.RedirectTo)
		clear(&d.Branch, req.Branch)
		clear(&d.ServiceName, req.ServiceName)
		if req.StatusCode != nil { // like Temps, there is no way to clear it
			d.StatusCode = req.StatusCode
		}
		writeJSON(w, 200, d)
	})
	h("DELETE /api/projects/{pid}/custom-domains/{id}", func(w http.ResponseWriter, r *http.Request) {
		if d := domain(w, r); d != nil {
			delete(x.customDomains, d.ID)
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// ------------------------------------------------------------ webhooks
	h("GET /api/webhook-event-types", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, []client.WebhookEventType{
			{EventType: "deployment.succeeded", Category: "Deployment"}, {EventType: "deployment.failed", Category: "Deployment"},
			{EventType: "domain.created", Category: "Domain"},
		})
	})
	h("POST /api/projects/{pid}/webhooks", func(w http.ResponseWriter, r *http.Request) {
		pid, ok := project(w, r)
		if !ok {
			return
		}
		var req client.WebhookRequest
		_ = decode(r, &req)
		wh := &fakeWebhook{w: client.Webhook{ID: f.nextID(), ProjectID: pid, URL: *req.URL, Events: *req.Events, Enabled: req.Enabled == nil || *req.Enabled}}
		if req.Secret != nil {
			wh.secret, wh.w.HasSecret = *req.Secret, true
		}
		x.webhooks[wh.w.ID] = wh
		writeJSON(w, 201, wh.w)
	})
	webhook := func(w http.ResponseWriter, r *http.Request) *fakeWebhook {
		wh, ok := x.webhooks[pathID(r, "id")]
		if !ok || wh.w.ProjectID != pathID(r, "pid") {
			problem(w, 404, "Webhook not found", "Webhook does not belong to this project")
			return nil
		}
		return wh
	}
	h("GET /api/projects/{pid}/webhooks/{id}", func(w http.ResponseWriter, r *http.Request) {
		if wh := webhook(w, r); wh != nil {
			writeJSON(w, 200, wh.w)
		}
	})
	h("PUT /api/projects/{pid}/webhooks/{id}", func(w http.ResponseWriter, r *http.Request) {
		wh := webhook(w, r)
		if wh == nil {
			return
		}
		var req client.WebhookRequest
		_ = decode(r, &req)
		if req.URL != nil {
			wh.w.URL = *req.URL
		}
		if req.Events != nil {
			wh.w.Events = *req.Events
		}
		if req.Enabled != nil {
			wh.w.Enabled = *req.Enabled
		}
		if req.Secret != nil {
			wh.secret, wh.w.HasSecret = *req.Secret, true
		}
		writeJSON(w, 200, wh.w)
	})
	h("DELETE /api/projects/{pid}/webhooks/{id}", func(w http.ResponseWriter, r *http.Request) {
		if wh := webhook(w, r); wh != nil {
			delete(x.webhooks, wh.w.ID)
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// --------------------------------------------------------------- flags
	h("POST /api/projects/{pid}/flags", func(w http.ResponseWriter, r *http.Request) {
		pid, ok := project(w, r)
		if !ok {
			return
		}
		var req client.CreateFeatureFlagRequest
		_ = decode(r, &req)
		k := fmt.Sprintf("%d/%s", pid, req.Key)
		if _, exists := x.flags[k]; exists { // archived keys stay reserved
			problem(w, 409, "Feature Flag Already Exists", fmt.Sprintf("Feature flag '%s' already exists in project %d", req.Key, pid))
			return
		}
		x.flags[k] = &client.FeatureFlag{ID: f.nextID(), Key: req.Key, ValueType: req.ValueType, DefaultValue: reencode(req.DefaultValue),
			Description: req.Description, ClientVisible: req.ClientVisible, Environments: []client.FeatureFlagEnvironment{}}
		writeJSON(w, 201, x.flags[k])
	})
	flag := func(w http.ResponseWriter, r *http.Request) *client.FeatureFlag {
		fl, ok := x.flags[fmt.Sprintf("%d/%s", pathID(r, "pid"), r.PathValue("key"))]
		if !ok {
			problem(w, 404, "Feature Flag Not Found", fmt.Sprintf("Feature flag '%s' not found in project %s", r.PathValue("key"), r.PathValue("pid")))
			return nil
		}
		return fl
	}
	h("GET /api/projects/{pid}/flags/{key}", func(w http.ResponseWriter, r *http.Request) {
		if fl := flag(w, r); fl != nil {
			writeJSON(w, 200, fl)
		}
	})
	h("PATCH /api/projects/{pid}/flags/{key}", func(w http.ResponseWriter, r *http.Request) {
		fl := flag(w, r)
		if fl == nil {
			return
		}
		var raw map[string]json.RawMessage
		_ = decode(r, &raw)
		if v, ok := raw["default_value"]; ok {
			fl.DefaultValue = reencode(v)
		}
		if v, ok := raw["description"]; ok {
			fl.Description = nil
			_ = json.Unmarshal(v, &fl.Description)
		}
		if v, ok := raw["client_visible"]; ok {
			_ = json.Unmarshal(v, &fl.ClientVisible)
		}
		writeJSON(w, 200, fl)
	})
	h("DELETE /api/projects/{pid}/flags/{key}", func(w http.ResponseWriter, r *http.Request) {
		if fl := flag(w, r); fl != nil {
			fl.ArchivedAt = ptr("2026-01-01T00:00:00Z")
			writeJSON(w, 200, map[string]any{"key": fl.Key, "archived_at": fl.ArchivedAt})
		}
	})
	h("POST /api/projects/{pid}/flags/{key}/restore", func(w http.ResponseWriter, r *http.Request) {
		if fl := flag(w, r); fl != nil {
			fl.ArchivedAt = nil
			writeJSON(w, 200, fl)
		}
	})
	h("PUT /api/projects/{pid}/flags/{key}/environments/{env}", func(w http.ResponseWriter, r *http.Request) {
		fl := flag(w, r)
		if fl == nil {
			return
		}
		env := pathID(r, "env")
		if !f.envOf(pathID(r, "pid"), env) {
			problem(w, 400, "Environment Not In Project", fmt.Sprintf("Environment %d does not belong to project %s", env, r.PathValue("pid")))
			return
		}
		var raw map[string]json.RawMessage
		_ = decode(r, &raw)
		i := slices.IndexFunc(fl.Environments, func(e client.FeatureFlagEnvironment) bool { return e.EnvironmentID == env })
		if i < 0 {
			fl.Environments = append(fl.Environments, client.FeatureFlagEnvironment{EnvironmentID: env, Enabled: true, Value: json.RawMessage("null")})
			i = len(fl.Environments) - 1
		}
		e := &fl.Environments[i]
		if v, ok := raw["enabled"]; ok && string(v) != "null" {
			_ = json.Unmarshal(v, &e.Enabled)
		}
		if v, ok := raw["value"]; ok {
			e.Value = reencode(v)
		}
		writeJSON(w, 200, e)
	})

	// ----------------------------------------------------------- IP access
	ipKey := func(r *http.Request) (string, int64, *int64) {
		pid := pathID(r, "pid")
		if env := r.PathValue("env"); env != "" {
			e := pathID(r, "env")
			return fmt.Sprintf("%d/%d", pid, e), pid, &e
		}
		return formatID(pid), pid, nil
	}
	getIP := func(w http.ResponseWriter, r *http.Request) {
		k, pid, env := ipKey(r)
		if p, ok := x.ipAccess[k]; ok {
			writeJSON(w, 200, p)
			return
		}
		writeJSON(w, 200, client.IPAccessPolicy{ProjectID: pid, EnvironmentID: env, Mode: "disabled", CIDRs: []client.IPAccessCIDR{}, Warnings: []string{}})
	}
	putIP := func(w http.ResponseWriter, r *http.Request) {
		k, pid, env := ipKey(r)
		if env != nil && !f.envOf(pid, *env) {
			problem(w, 404, "Environment Not Found", fmt.Sprintf("Environment %d not found in project %d", *env, pid))
			return
		}
		var req client.SetIPAccessPolicyRequest
		_ = decode(r, &req)
		for i, c := range req.CIDRs {
			canon, bad := canonicalizeCIDR(c.CIDR)
			if bad != "" {
				problem(w, 400, "Invalid IP Access Policy", "Invalid CIDR: "+bad)
				return
			}
			req.CIDRs[i].CIDR = canon
		}
		mine := slices.ContainsFunc(req.CIDRs, func(c client.IPAccessCIDR) bool { return c.CIDR == x.callerIP+"/32" })
		if !req.AcknowledgeLockout && ((req.Mode == "deny" && mine) || (req.Mode == "allow" && !mine)) {
			problem(w, 409, "Policy Would Lock You Out", "Saving this policy would block your current address "+x.callerIP+"; resubmit with acknowledge_lockout=true")
			return
		}
		p := &client.IPAccessPolicy{ProjectID: pid, EnvironmentID: env, Configured: true, Mode: req.Mode, CIDRs: req.CIDRs, Warnings: []string{}}
		x.ipAccess[k] = p
		writeJSON(w, 200, p)
	}
	delIP := func(w http.ResponseWriter, r *http.Request) {
		k, _, _ := ipKey(r)
		if _, ok := x.ipAccess[k]; !ok {
			problem(w, 404, "IP Access Policy Not Found", "No IP access policy is configured for "+k)
			return
		}
		delete(x.ipAccess, k)
		w.WriteHeader(http.StatusNoContent)
	}
	h("GET /api/projects/{pid}/ip-access", getIP)
	h("PUT /api/projects/{pid}/ip-access", putIP)
	h("DELETE /api/projects/{pid}/ip-access", delIP)
	h("GET /api/projects/{pid}/environments/{env}/ip-access", getIP)
	h("PUT /api/projects/{pid}/environments/{env}/ip-access", putIP)
	h("DELETE /api/projects/{pid}/environments/{env}/ip-access", delIP)

	// -------------------------------------------------------- cron monitors
	withDefaults := func(c *client.CronMonitorConfig) { // a PATCH resets omitted fields, like Temps
		if c.Timezone == "" {
			c.Timezone = "UTC"
		}
		for _, p := range []struct {
			v   **int64
			def int64
		}{{&c.CheckinMargin, 1}, {&c.MaxRuntime, 30}, {&c.FailureIssueThreshold, 1}, {&c.RecoveryThreshold, 1}} {
			if *p.v == nil {
				*p.v = ptr(p.def)
			}
		}
	}
	h("POST /api/projects/{pid}/cron-monitors", func(w http.ResponseWriter, r *http.Request) {
		pid, ok := project(w, r)
		if !ok {
			return
		}
		var req client.CreateCronMonitorRequest
		_ = decode(r, &req)
		for _, c := range x.crons {
			if c.ProjectID == pid && c.Slug == req.Slug && (c.EnvironmentID == nil) == (req.EnvironmentID == nil) {
				problem(w, 409, "Cron monitor operation failed", "Cron monitor already exists in this project and environment")
				return
			}
		}
		withDefaults(&req.Config)
		c := &client.CronMonitor{ID: f.nextID(), ProjectID: pid, EnvironmentID: req.EnvironmentID, Slug: req.Slug, Config: &req.Config,
			Enabled: true, ConfigurationSource: "temps", Status: "unknown"}
		x.crons[c.ID] = c
		writeJSON(w, 201, c)
	})
	cron := func(w http.ResponseWriter, r *http.Request) *client.CronMonitor {
		c, ok := x.crons[pathID(r, "id")]
		if !ok || c.ProjectID != pathID(r, "pid") {
			problem(w, 404, "Cron monitor operation failed", fmt.Sprintf("Cron monitor %s was not found in project %s", r.PathValue("id"), r.PathValue("pid")))
			return nil
		}
		return c
	}
	h("GET /api/projects/{pid}/cron-monitors/{id}", func(w http.ResponseWriter, r *http.Request) {
		if c := cron(w, r); c != nil {
			writeJSON(w, 200, c)
		}
	})
	h("PATCH /api/projects/{pid}/cron-monitors/{id}", func(w http.ResponseWriter, r *http.Request) {
		c := cron(w, r)
		if c == nil {
			return
		}
		var req client.UpdateCronMonitorRequest
		_ = decode(r, &req)
		if req.Config != nil {
			withDefaults(req.Config)
			c.Config = req.Config
		}
		if req.Enabled != nil {
			c.Enabled = *req.Enabled
			if !c.Enabled {
				c.Status = "paused"
			}
		}
		writeJSON(w, 200, c)
	})
	h("DELETE /api/projects/{pid}/cron-monitors/{id}", func(w http.ResponseWriter, r *http.Request) {
		if c := cron(w, r); c != nil {
			delete(x.crons, c.ID)
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// ---------------------------------------------------- deployment tokens
	h("POST /api/projects/{pid}/deployment-tokens", func(w http.ResponseWriter, r *http.Request) {
		pid, ok := project(w, r)
		if !ok {
			return
		}
		var req client.CreateDeploymentTokenRequest
		_ = decode(r, &req)
		perms := []string{"*"}
		if req.Permissions != nil {
			perms = *req.Permissions
		}
		t := &client.DeploymentToken{ID: f.nextID(), ProjectID: pid, EnvironmentID: req.EnvironmentID, Name: req.Name, Permissions: &perms,
			IsActive: ptr(true), ExpiresAt: utc(req.ExpiresAt)}
		t.TokenPrefix = fmt.Sprintf("dt_%05d", t.ID)
		x.tokens[t.ID] = t
		out := *t
		out.Token, out.IsActive = t.TokenPrefix+"secretvalue", nil
		writeJSON(w, 201, out)
	})
	token := func(w http.ResponseWriter, r *http.Request) *client.DeploymentToken {
		t, ok := x.tokens[pathID(r, "id")]
		if !ok || t.ProjectID != pathID(r, "pid") {
			notFound(w, "deployment token", r.PathValue("id"))
			return nil
		}
		return t
	}
	h("GET /api/projects/{pid}/deployment-tokens/{id}", func(w http.ResponseWriter, r *http.Request) {
		if t := token(w, r); t != nil {
			writeJSON(w, 200, t)
		}
	})
	h("PATCH /api/projects/{pid}/deployment-tokens/{id}", func(w http.ResponseWriter, r *http.Request) {
		t := token(w, r)
		if t == nil {
			return
		}
		var req client.UpdateDeploymentTokenRequest
		_ = decode(r, &req)
		if req.Name != nil {
			t.Name = *req.Name
		}
		if req.IsActive != nil {
			t.IsActive = req.IsActive
		}
		if req.ExpiresAt != nil {
			t.ExpiresAt = utc(req.ExpiresAt)
		}
		if req.Permissions != nil {
			t.Permissions = req.Permissions
		}
		writeJSON(w, 200, t)
	})
	h("DELETE /api/projects/{pid}/deployment-tokens/{id}", func(w http.ResponseWriter, r *http.Request) {
		if t := token(w, r); t != nil {
			delete(x.tokens, t.ID)
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// --------------------------------------------------------------- teams
	h("POST /api/teams", func(w http.ResponseWriter, r *http.Request) {
		var req client.TeamRequest
		_ = decode(r, &req)
		for _, t := range x.teams {
			if t.Slug == *req.Slug {
				problem(w, 409, "Resource Conflict", "Team slug '"+t.Slug+"' is already taken")
				return
			}
		}
		t := &client.Team{ID: f.nextID(), Name: *req.Name, Slug: *req.Slug, Description: req.Description}
		x.teams[t.ID] = t
		writeJSON(w, 201, t)
	})
	team := func(w http.ResponseWriter, r *http.Request) *client.Team {
		t, ok := x.teams[pathID(r, "id")]
		if !ok {
			notFound(w, "team", r.PathValue("id"))
			return nil
		}
		return t
	}
	h("GET /api/teams/{id}", func(w http.ResponseWriter, r *http.Request) {
		if t := team(w, r); t != nil {
			writeJSON(w, 200, t)
		}
	})
	h("PATCH /api/teams/{id}", func(w http.ResponseWriter, r *http.Request) {
		t := team(w, r)
		if t == nil {
			return
		}
		var req client.TeamRequest
		_ = decode(r, &req)
		if req.Name != nil {
			t.Name = *req.Name
		}
		if req.Description != nil { // null keeps; "" is stored as ""
			t.Description = req.Description
		}
		writeJSON(w, 200, t)
	})
	h("DELETE /api/teams/{id}", func(w http.ResponseWriter, r *http.Request) {
		if t := team(w, r); t != nil {
			delete(x.teams, t.ID)
			for k, a := range x.access {
				if a.TeamID == t.ID {
					delete(x.access, k)
				}
			}
			w.WriteHeader(http.StatusNoContent)
		}
	})
	h("GET /api/projects/{pid}/access", func(w http.ResponseWriter, r *http.Request) {
		out := []client.ProjectAccess{}
		for _, a := range x.access {
			if a.ProjectID == pathID(r, "pid") {
				out = append(out, *a)
			}
		}
		writeJSON(w, 200, out)
	})
	h("POST /api/projects/{pid}/access", func(w http.ResponseWriter, r *http.Request) {
		pid, ok := project(w, r)
		if !ok {
			return
		}
		var req struct {
			TeamID int64  `json:"team_id"`
			Role   string `json:"role"`
		}
		_ = decode(r, &req)
		if _, ok := x.teams[req.TeamID]; !ok {
			notFound(w, "team", req.TeamID)
			return
		}
		k := fmt.Sprintf("%d/%d", pid, req.TeamID)
		a, ok := x.access[k]
		if !ok { // upsert, like Temps
			a = &client.ProjectAccess{ID: f.nextID(), ProjectID: pid, TeamID: req.TeamID}
			x.access[k] = a
		}
		a.Role = req.Role
		writeJSON(w, 201, a)
	})
	h("DELETE /api/projects/{pid}/access/{team}", func(w http.ResponseWriter, r *http.Request) {
		k := fmt.Sprintf("%d/%d", pathID(r, "pid"), pathID(r, "team"))
		if _, ok := x.access[k]; !ok {
			problem(w, 404, "Resource Not Found", "Project "+r.PathValue("pid")+" has no access grant for team "+r.PathValue("team"))
			return
		}
		delete(x.access, k)
		w.WriteHeader(http.StatusNoContent)
	})
}

// reencode normalises JSON the way the server does (sorted keys, shortest numbers).
func reencode(raw json.RawMessage) json.RawMessage {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	b, _ := json.Marshal(v)
	return b
}

// utc rewrites a timestamp to UTC, as the server does.
func utc(s *string) *string {
	if s == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return s
	}
	return ptr(t.UTC().Format(time.RFC3339))
}
