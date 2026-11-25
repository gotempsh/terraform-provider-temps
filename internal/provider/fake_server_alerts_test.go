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

// fakeAlerts holds project secrets, notification routes, error alert rules
// and edge rules, with the real server's quirks the provider copes with.
type fakeAlerts struct {
	secrets map[int64]*fakeSecret
	routes  map[int64]*client.NotificationRoute
	rules   map[int64]*client.ErrorAlertRule
	edges   map[int64]*client.EdgeRule
	apiKeys map[int64]*client.APIKey
	filters map[int64]*client.InboundFilter
	// seeded records which projects already got their default alert rules.
	seeded map[int64]bool
}

type fakeSecret struct {
	s     client.ProjectSecret
	value string
}

func newFakeAlerts() *fakeAlerts {
	return &fakeAlerts{secrets: map[int64]*fakeSecret{}, routes: map[int64]*client.NotificationRoute{},
		rules: map[int64]*client.ErrorAlertRule{}, edges: map[int64]*client.EdgeRule{}, apiKeys: map[int64]*client.APIKey{}, filters: map[int64]*client.InboundFilter{}, seeded: map[int64]bool{}}
}

func (f *fakeTemps) alertRoutes(h func(string, func(http.ResponseWriter, *http.Request))) {
	a := f.al
	project := func(w http.ResponseWriter, r *http.Request) (int64, bool) {
		pid := pathID(r, "pid")
		if _, ok := f.projects[pid]; !ok {
			notFound(w, "project", pid)
			return 0, false
		}
		return pid, true
	}

	// --------------------------------------------------------------- secrets
	// Like Temps: no single GET, an empty scope means every environment, and a
	// PUT treats omitted scope fields as empty, widening the secret.
	setScope := func(w http.ResponseWriter, pid int64, s *client.ProjectSecret, req client.ProjectSecretRequest) bool {
		s.Environments = s.Environments[:0]
		for _, id := range req.EnvironmentIDs {
			if !f.envOf(pid, id) {
				problem(w, 404, "Resource Not Found", fmt.Sprintf("Environment %d was not found in project %d", id, pid))
				return false
			}
			s.Environments = append(s.Environments, struct {
				ID int64 `json:"id"`
			}{ID: id})
		}
		s.IncludeInPreview = req.IncludeInPreview
		s.ComposeServices = append([]string{}, req.ComposeServices...)
		return true
	}
	overlaps := func(pid int64, key string, envs []int64, self int64) bool {
		for _, o := range a.secrets {
			if o.s.ProjectID != pid || o.s.Key != key || o.s.ID == self {
				continue
			}
			other := o.s.EnvironmentIDs()
			if len(envs) == 0 || len(other) == 0 {
				return true
			}
			for _, e := range envs {
				if slices.Contains(other, e) {
					return true
				}
			}
		}
		return false
	}
	h("GET /api/projects/{pid}/secrets", func(w http.ResponseWriter, r *http.Request) {
		pid := pathID(r, "pid")
		out := []client.ProjectSecret{}
		for _, s := range a.secrets {
			if s.s.ProjectID == pid {
				out = append(out, s.s)
			}
		}
		writeJSON(w, 200, out)
	})
	h("POST /api/projects/{pid}/secrets", func(w http.ResponseWriter, r *http.Request) {
		pid, ok := project(w, r)
		if !ok {
			return
		}
		var req client.ProjectSecretRequest
		_ = decode(r, &req)
		if overlaps(pid, req.Key, req.EnvironmentIDs, 0) {
			problem(w, 409, "Conflict", fmt.Sprintf("Secret with key '%s' already applies to one or more requested environments in project %d", req.Key, pid))
			return
		}
		s := &fakeSecret{s: client.ProjectSecret{ID: f.nextID(), ProjectID: pid, Key: req.Key}}
		if req.Value != nil {
			s.value = *req.Value
		}
		if !setScope(w, pid, &s.s, req) {
			return
		}
		a.secrets[s.s.ID] = s
		writeJSON(w, 201, s.s)
	})
	secret := func(w http.ResponseWriter, r *http.Request) *fakeSecret {
		s, ok := a.secrets[pathID(r, "id")]
		if !ok || s.s.ProjectID != pathID(r, "pid") {
			problem(w, 404, "Resource Not Found", fmt.Sprintf("Secret %s not found in project %s", r.PathValue("id"), r.PathValue("pid")))
			return nil
		}
		return s
	}
	h("PUT /api/projects/{pid}/secrets/{id}", func(w http.ResponseWriter, r *http.Request) {
		s := secret(w, r)
		if s == nil {
			return
		}
		var req client.ProjectSecretRequest
		_ = decode(r, &req)
		if overlaps(s.s.ProjectID, s.s.Key, req.EnvironmentIDs, s.s.ID) {
			problem(w, 409, "Conflict", "Secret scope overlaps another secret with the same key")
			return
		}
		if !setScope(w, s.s.ProjectID, &s.s, req) {
			return
		}
		if req.Value != nil {
			s.value = *req.Value
		}
		writeJSON(w, 200, s.s)
	})
	h("DELETE /api/projects/{pid}/secrets/{id}", func(w http.ResponseWriter, r *http.Request) {
		if s := secret(w, r); s != nil {
			delete(a.secrets, s.s.ID)
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// --------------------------------------------------- notification routes
	// Deleting a provider strips it from routes (see the provider delete hook).
	validRoute := func(w http.ResponseWriter, req client.NotificationRouteRequest) bool {
		if len(req.ProviderIDs) == 0 {
			problem(w, 400, "Invalid notification route", "A notification route must contain at least one provider")
			return false
		}
		for _, id := range req.ProviderIDs {
			if _, ok := f.x.notifiers[id]; !ok {
				problem(w, 400, "Invalid notification route", fmt.Sprintf("Notification provider %d assigned to the route was not found", id))
				return false
			}
		}
		return true
	}
	h("POST /api/notification-routes", func(w http.ResponseWriter, r *http.Request) {
		var req client.NotificationRouteRequest
		_ = decode(r, &req)
		if !validRoute(w, req) {
			return
		}
		rt := &client.NotificationRoute{ID: f.nextID(), Name: req.Name, Enabled: req.Enabled, MinSeverity: req.MinSeverity,
			MaxSeverity: req.MaxSeverity, ProviderIDs: dedupe(req.ProviderIDs)}
		a.routes[rt.ID] = rt
		writeJSON(w, 201, rt)
	})
	route := func(w http.ResponseWriter, r *http.Request) *client.NotificationRoute {
		rt, ok := a.routes[pathID(r, "id")]
		if !ok {
			problem(w, 404, "Notification route not found", fmt.Sprintf("Notification route %s was not found", r.PathValue("id")))
			return nil
		}
		return rt
	}
	h("GET /api/notification-routes/{id}", func(w http.ResponseWriter, r *http.Request) {
		if rt := route(w, r); rt != nil {
			writeJSON(w, 200, rt)
		}
	})
	h("PUT /api/notification-routes/{id}", func(w http.ResponseWriter, r *http.Request) {
		rt := route(w, r)
		if rt == nil {
			return
		}
		var req client.NotificationRouteRequest
		_ = decode(r, &req)
		if !validRoute(w, req) {
			return
		}
		rt.Name, rt.Enabled, rt.MinSeverity, rt.MaxSeverity = req.Name, req.Enabled, req.MinSeverity, req.MaxSeverity
		rt.ProviderIDs = dedupe(req.ProviderIDs)
		writeJSON(w, 200, rt)
	})
	h("DELETE /api/notification-routes/{id}", func(w http.ResponseWriter, r *http.Request) {
		if rt := route(w, r); rt != nil {
			delete(a.routes, rt.ID)
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// ------------------------------------------------------ error alert rules
	// Every project has two default rules; another project's rule is a 400;
	// null filters on update are ignored.
	seed := func(pid int64) {
		if a.seeded[pid] {
			return
		}
		a.seeded[pid] = true
		for _, d := range [][2]string{{"New issue detected", "new_issue"}, {"Regression detected", "regression"}} {
			id := f.nextID()
			a.rules[id] = &client.ErrorAlertRule{ID: id, ProjectID: pid, Name: d[0], TriggerType: d[1],
				TriggerConfig: json.RawMessage(`{}`), NotificationPriority: "High", CooldownMinutes: 30, Enabled: true}
		}
	}
	h("POST /api/projects/{pid}/error-alert-rules", func(w http.ResponseWriter, r *http.Request) {
		pid, ok := project(w, r)
		if !ok {
			return
		}
		seed(pid)
		var req client.ErrorAlertRuleRequest
		_ = decode(r, &req)
		rule := &client.ErrorAlertRule{ID: f.nextID(), ProjectID: pid, Name: req.Name, TriggerType: req.TriggerType,
			TriggerConfig: reencode(req.TriggerConfig), EnvironmentFilter: req.EnvironmentFilter, ErrorLevelFilter: req.ErrorLevelFilter,
			NotificationPriority: req.NotificationPriority, CooldownMinutes: req.CooldownMinutes, Enabled: req.Enabled}
		a.rules[rule.ID] = rule
		writeJSON(w, 201, rule)
	})
	rule := func(w http.ResponseWriter, r *http.Request) *client.ErrorAlertRule {
		rule, ok := a.rules[pathID(r, "id")]
		if !ok || rule.ProjectID != pathID(r, "pid") {
			problem(w, 400, "Validation Error", fmt.Sprintf("Validation error: Alert rule %s not found in project %s", r.PathValue("id"), r.PathValue("pid")))
			return nil
		}
		return rule
	}
	h("GET /api/projects/{pid}/error-alert-rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if rule := rule(w, r); rule != nil {
			writeJSON(w, 200, rule)
		}
	})
	h("PUT /api/projects/{pid}/error-alert-rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		rule := rule(w, r)
		if rule == nil {
			return
		}
		var req client.ErrorAlertRuleRequest
		_ = decode(r, &req)
		rule.Name, rule.TriggerType, rule.TriggerConfig = req.Name, req.TriggerType, reencode(req.TriggerConfig)
		if req.EnvironmentFilter != nil {
			rule.EnvironmentFilter = req.EnvironmentFilter
		}
		if req.ErrorLevelFilter != nil {
			rule.ErrorLevelFilter = req.ErrorLevelFilter
		}
		rule.NotificationPriority, rule.CooldownMinutes, rule.Enabled = req.NotificationPriority, req.CooldownMinutes, req.Enabled
		writeJSON(w, 200, rule)
	})
	h("DELETE /api/projects/{pid}/error-alert-rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if rule := rule(w, r); rule != nil {
			delete(a.rules, rule.ID)
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// ------------------------------------------------------------ edge rules
	// The server adds node ids and defaults to the expression, keeps a
	// revision and refuses a stale one with 409.
	annotate := func(raw json.RawMessage) json.RawMessage {
		var v any
		_ = json.Unmarshal(raw, &v)
		n := 0
		var walk func(any)
		walk = func(x any) {
			m, ok := x.(map[string]any)
			if !ok {
				return
			}
			n++
			m["id"] = fmt.Sprintf("n%d", n)
			if m["kind"] == "condition" {
				if _, ok := m["case_insensitive"]; !ok {
					m["case_insensitive"] = false
				}
				if _, ok := m["values"]; !ok {
					m["values"] = []any{}
				}
			}
			if children, ok := m["children"].([]any); ok {
				for _, c := range children {
					walk(c)
				}
			}
		}
		walk(v)
		b, _ := json.Marshal(v)
		return b
	}
	summary := func(e *client.EdgeRule) string {
		return strings.ToUpper(e.Action.Type[:1]) + e.Action.Type[1:] + " matching requests"
	}
	h("POST /api/projects/{pid}/edge-rules", func(w http.ResponseWriter, r *http.Request) {
		pid, ok := project(w, r)
		if !ok {
			return
		}
		var req client.EdgeRuleRequest
		_ = decode(r, &req)
		if req.EnvironmentID != nil && !f.envOf(pid, *req.EnvironmentID) {
			problem(w, 404, "Not Found", fmt.Sprintf("Environment %d not found in project %d", *req.EnvironmentID, pid))
			return
		}
		pos := int64(0)
		for _, e := range a.edges {
			if e.ProjectID == pid {
				pos++
			}
		}
		e := &client.EdgeRule{ID: f.nextID(), ProjectID: pid, EnvironmentID: req.EnvironmentID, Position: pos, Name: req.Name,
			Enabled: req.Enabled, Mode: req.Mode, Expression: annotate(req.Expression), Action: req.Action, Revision: 1}
		if e.Action.Type == "block" && e.Action.Body == nil {
			e.Action.Body = ptr("")
		}
		e.Summary = summary(e)
		a.edges[e.ID] = e
		writeJSON(w, 201, e)
	})
	edge := func(w http.ResponseWriter, r *http.Request) *client.EdgeRule {
		e, ok := a.edges[pathID(r, "id")]
		if !ok || e.ProjectID != pathID(r, "pid") {
			problem(w, 404, "Not Found", fmt.Sprintf("Edge rule %s not found in project %s", r.PathValue("id"), r.PathValue("pid")))
			return nil
		}
		return e
	}
	h("GET /api/projects/{pid}/edge-rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if e := edge(w, r); e != nil {
			writeJSON(w, 200, e)
		}
	})
	h("PATCH /api/projects/{pid}/edge-rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		e := edge(w, r)
		if e == nil {
			return
		}
		var req client.EdgeRuleRequest
		_ = decode(r, &req)
		if req.Revision != nil && *req.Revision != e.Revision {
			problem(w, 409, "Conflict", fmt.Sprintf("Edge rule %d in project %d was changed by someone else (you edited revision %d, it is now at %d)",
				e.ID, e.ProjectID, *req.Revision, e.Revision))
			return
		}
		e.Name, e.EnvironmentID, e.Enabled, e.Mode = req.Name, req.EnvironmentID, req.Enabled, req.Mode
		e.Expression, e.Action = annotate(req.Expression), req.Action
		if e.Action.Type == "block" && e.Action.Body == nil {
			e.Action.Body = ptr("")
		}
		e.Revision++
		e.Summary = summary(e)
		writeJSON(w, 200, e)
	})
	h("DELETE /api/projects/{pid}/edge-rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if e := edge(w, r); e != nil {
			delete(a.edges, e.ID)
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

// apiKeyRoutes mimics /api-keys: a default expiry one year ahead, the key
// shown once, unique names, and permissions ignored on update.
func (f *fakeTemps) apiKeyRoutes(h func(string, func(http.ResponseWriter, *http.Request))) {
	a := f.al
	h("POST /api/api-keys", func(w http.ResponseWriter, r *http.Request) {
		var req client.CreateAPIKeyRequest
		_ = decode(r, &req)
		for _, k := range a.apiKeys {
			if k.Name == req.Name {
				problem(w, 409, "Conflict", "API key with this name already exists")
				return
			}
		}
		if req.RoleType == "custom" && (req.Permissions == nil || len(*req.Permissions) == 0) {
			problem(w, 400, "Bad Request", "Custom role requires at least one permission")
			return
		}
		k := &client.APIKey{ID: f.nextID(), Name: req.Name, RoleType: req.RoleType, ProjectID: req.ProjectID,
			EnvironmentID: req.EnvironmentID, IsActive: ptr(true), ExpiresAt: utc(req.ExpiresAt)}
		if req.RoleType == "custom" {
			k.Permissions = req.Permissions
		}
		if k.ExpiresAt == nil {
			k.ExpiresAt = ptr(time.Now().AddDate(1, 0, 0).UTC().Format(time.RFC3339Nano))
		}
		k.KeyPrefix = fmt.Sprintf("tk_%05d", k.ID)
		out := *k
		out.APIKey = k.KeyPrefix + "secret"
		a.apiKeys[k.ID] = k
		writeJSON(w, 201, out)
	})
	key := func(w http.ResponseWriter, r *http.Request) *client.APIKey {
		k, ok := a.apiKeys[pathID(r, "id")]
		if !ok {
			notFound(w, "API key", r.PathValue("id"))
			return nil
		}
		return k
	}
	h("GET /api/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		if k := key(w, r); k != nil {
			writeJSON(w, 200, k)
		}
	})
	h("PATCH /api/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		k := key(w, r)
		if k == nil {
			return
		}
		var req client.UpdateAPIKeyRequest
		_ = decode(r, &req)
		if req.Name != nil {
			k.Name = *req.Name
		}
		if req.IsActive != nil {
			k.IsActive = req.IsActive
		}
		if req.ExpiresAt != nil {
			k.ExpiresAt = utc(req.ExpiresAt)
		}
		writeJSON(w, 200, k)
	})
	h("DELETE /api/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		if k := key(w, r); k != nil {
			delete(a.apiKeys, k.ID)
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

// inboundFilterRoutes mimics /projects/{id}/error-inbound-filters: built-in
// kinds once per project (409), patterns required only for pattern kinds,
// and PATCH ignoring a null pattern.
func (f *fakeTemps) inboundFilterRoutes(h func(string, func(http.ResponseWriter, *http.Request))) {
	a := f.al
	base := "/api/projects/{pid}/error-inbound-filters"
	patternKind := func(k string) bool { return k == "error_message" || k == "release" }
	h("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		pid := pathID(r, "pid")
		out := []client.InboundFilter{}
		for _, fl := range a.filters {
			if fl.ProjectID == pid {
				out = append(out, *fl)
			}
		}
		writeJSON(w, 200, map[string]any{"filters": out, "limit": 50, "max_pattern_chars": 200})
	})
	h("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		pid := pathID(r, "pid")
		if _, ok := f.projects[pid]; !ok {
			problem(w, 500, "Internal Server Error", "foreign key constraint")
			return
		}
		var req client.InboundFilterRequest
		_ = decode(r, &req)
		hasPattern := req.Pattern != nil && strings.TrimSpace(*req.Pattern) != ""
		switch {
		case patternKind(req.Kind) && !hasPattern:
			problem(w, 400, "Validation Error", "needs a pattern")
			return
		case !patternKind(req.Kind) && req.Pattern != nil:
			problem(w, 400, "Validation Error", "takes no pattern")
			return
		}
		if !patternKind(req.Kind) {
			for _, fl := range a.filters {
				if fl.ProjectID == pid && fl.Kind == req.Kind {
					problem(w, 409, "Inbound Filter Already Exists", "already has this filter")
					return
				}
			}
		}
		fl := &client.InboundFilter{ID: f.nextID(), ProjectID: pid, Kind: req.Kind, Pattern: req.Pattern, Enabled: true}
		if req.Enabled != nil {
			fl.Enabled = *req.Enabled
		}
		a.filters[fl.ID] = fl
		writeJSON(w, 201, fl)
	})
	filter := func(w http.ResponseWriter, r *http.Request) *client.InboundFilter {
		fl, ok := a.filters[pathID(r, "id")]
		if !ok || fl.ProjectID != pathID(r, "pid") {
			notFound(w, "inbound filter", r.PathValue("id"))
			return nil
		}
		return fl
	}
	h("PATCH "+base+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		fl := filter(w, r)
		if fl == nil {
			return
		}
		var req client.InboundFilterRequest
		_ = decode(r, &req)
		if req.Pattern != nil {
			if !patternKind(fl.Kind) {
				problem(w, 400, "Validation Error", "takes no pattern")
				return
			}
			fl.Pattern = req.Pattern
		}
		if req.Enabled != nil {
			fl.Enabled = *req.Enabled
		}
		writeJSON(w, 200, fl)
	})
	h("DELETE "+base+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		if fl := filter(w, r); fl != nil {
			delete(a.filters, fl.ID)
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

func dedupe(ids []int64) []int64 {
	out := []int64{}
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
