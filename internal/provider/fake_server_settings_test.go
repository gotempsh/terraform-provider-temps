// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

// fakeSettings holds settings that live on their own endpoints of an
// existing object: a service's healthcheck, TLS, log budget and workload
// mode, and a domain's certificate settings.
type fakeSettings struct {
	health   map[int64]*client.HealthcheckOverrides
	applied  map[int64]int // healthcheck applies (container recreates) per service
	tls      map[int64]string
	tlsSets  map[int64]int
	logs     map[int64]*client.ServiceLogIngestion
	workload map[int64]string
	// notAdmin makes the workload mode endpoints answer 403, as for a key
	// that is not an instance administrator's.
	notAdmin bool
	// old makes the settings endpoints answer like a server that predates
	// them: a bare 404 without problem details.
	old bool

	certDomains map[int64]map[string]any // domain ID -> domain document
	certs       map[int64]map[string]any // domain ID -> stored settings

	rev       int // revision counter of flag environment rows
	ruleSets  int // successful rule replacements
	bumpRules bool
}

// touchFlagEnv gives a written flag environment row a new revision, as
// Temps does with updated_at.
func (st *fakeSettings) touchFlagEnv(e *client.FeatureFlagEnvironment) {
	st.rev++
	e.UpdatedAt = fmt.Sprintf("2026-01-01T00:00:%02d.000000Z", st.rev%60) + fmt.Sprint(st.rev)
	if e.Rules == nil && !st.old {
		e.Rules = &[]client.FlagRule{}
		e.RulesValid = ptr(true)
	}
}

func newFakeSettings() *fakeSettings {
	return &fakeSettings{
		health: map[int64]*client.HealthcheckOverrides{}, applied: map[int64]int{}, tls: map[int64]string{},
		tlsSets: map[int64]int{}, logs: map[int64]*client.ServiceLogIngestion{}, workload: map[int64]string{},
		certDomains: map[int64]map[string]any{}, certs: map[int64]map[string]any{},
	}
}

func inRange(v *int64, lo, hi int64) bool { return v == nil || (*v >= lo && *v <= hi) }

func (f *fakeTemps) settingsRoutes(route func(string, func(http.ResponseWriter, *http.Request))) {
	st := f.st
	h := func(pattern string, fn func(http.ResponseWriter, *http.Request)) {
		isNew := !strings.HasPrefix(pattern, "POST /api/domains") && strings.Count(pattern, "/") > 3
		route(pattern, func(w http.ResponseWriter, r *http.Request) {
			if st.old && isNew {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fn(w, r)
		})
	}
	svc := func(w http.ResponseWriter, r *http.Request) *fakeService {
		s, ok := f.services[pathID(r, "id")]
		if !ok {
			problem(w, 404, "Service Not Found", fmt.Sprintf("External service %s not found", r.PathValue("id")))
			return nil
		}
		return s
	}
	// Only standalone databases have a Docker healthcheck Temps builds.
	configurable := func(s *fakeService) bool { return s.s.ServiceType != "s3" && s.s.Topology == "standalone" }
	healthOf := func(s *fakeService) map[string]any {
		o := st.health[s.s.ID]
		if o == nil {
			o = &client.HealthcheckOverrides{}
		}
		return map[string]any{
			"service_id": s.s.ID, "service_name": s.s.Name, "service_type": s.s.ServiceType, "overrides": o,
			"capability":       map[string]any{"configurable": configurable(s), "can_apply_now": configurable(s) && s.s.Status == "running"},
			"pending_recreate": st.applied[s.s.ID] == 0 && st.health[s.s.ID] != nil,
		}
	}
	h("GET /api/external-services/{id}/healthcheck", func(w http.ResponseWriter, r *http.Request) {
		if s := svc(w, r); s != nil {
			writeJSON(w, 200, healthOf(s))
		}
	})
	h("PUT /api/external-services/{id}/healthcheck", func(w http.ResponseWriter, r *http.Request) {
		s := svc(w, r)
		if s == nil {
			return
		}
		var o client.HealthcheckOverrides
		_ = decode(r, &o)
		if !inRange(o.IntervalSecs, 5, 3600) || !inRange(o.TimeoutSecs, 1, 60) || !inRange(o.Retries, 1, 10) {
			problem(w, 400, "Invalid Healthcheck", "a value is out of range")
			return
		}
		if o.TimeoutSecs != nil && o.IntervalSecs != nil && *o.TimeoutSecs >= *o.IntervalSecs {
			problem(w, 400, "Invalid Healthcheck", "the timeout must be shorter than the interval")
			return
		}
		empty := o == client.HealthcheckOverrides{}
		if !empty && !configurable(s) {
			problem(w, 409, "Healthcheck Not Available For This Service",
				fmt.Sprintf("Cannot override the healthcheck of service %d (%s): its engine's container has no Docker healthcheck", s.s.ID, s.s.Name))
			return
		}
		st.health[s.s.ID] = &o
		st.applied[s.s.ID] = 0
		writeJSON(w, 200, healthOf(s))
	})
	h("POST /api/external-services/{id}/healthcheck/apply", func(w http.ResponseWriter, r *http.Request) {
		s := svc(w, r)
		if s == nil {
			return
		}
		if !configurable(s) || s.s.Status != "running" {
			problem(w, 409, "Healthcheck Not Available For This Service", "Temps will not recreate this container from here")
			return
		}
		st.applied[s.s.ID]++
		writeJSON(w, 200, map[string]any{"service_id": s.s.ID, "applied": map[string]any{}, "healthcheck": healthOf(s)})
	})

	tlsOf := func(s *fakeService) map[string]any {
		mode := st.tls[s.s.ID]
		if mode == "" {
			mode = "off"
		}
		return map[string]any{"service_id": s.s.ID, "mode": mode, "supported": s.s.ServiceType == "postgres",
			"accepts_plaintext": true, "change_restarts_database": true, "pending_restart": false, "members": []any{}}
	}
	h("GET /api/external-services/{id}/tls", func(w http.ResponseWriter, r *http.Request) {
		if s := svc(w, r); s != nil {
			writeJSON(w, 200, tlsOf(s))
		}
	})
	h("PUT /api/external-services/{id}/tls", func(w http.ResponseWriter, r *http.Request) {
		s := svc(w, r)
		if s == nil {
			return
		}
		var req struct {
			Mode string `json:"mode"`
		}
		_ = decode(r, &req)
		switch {
		case s.s.ServiceType != "postgres":
			problem(w, 400, "Server TLS Unsupported", "managed TLS is only available for PostgreSQL")
			return
		case req.Mode == "required":
			problem(w, 400, "Server TLS Mode Unsupported", "mode 'required' is not supported yet")
			return
		}
		prev := tlsOf(s)["mode"]
		st.tls[s.s.ID] = req.Mode
		st.tlsSets[s.s.ID]++
		// The database restarts to apply it.
		s.s.Status, s.gets = "pending", 0
		writeJSON(w, 200, map[string]any{"previous_mode": prev, "tls": tlsOf(s)})
	})

	logOf := func(s *fakeService) map[string]any {
		l := st.logs[s.s.ID]
		if l == nil {
			l = &client.ServiceLogIngestion{}
		}
		return map[string]any{"service_id": s.s.ID, "log_ingest_lines_per_sec": l.LinesPerSec, "log_ingest_kib_per_sec": l.KiBPerSec}
	}
	h("GET /api/external-services/{id}/log-ingestion", func(w http.ResponseWriter, r *http.Request) {
		if s := svc(w, r); s != nil {
			writeJSON(w, 200, logOf(s))
		}
	})
	h("PUT /api/external-services/{id}/log-ingestion", func(w http.ResponseWriter, r *http.Request) {
		s := svc(w, r)
		if s == nil {
			return
		}
		var l client.ServiceLogIngestion
		_ = decode(r, &l)
		if !inRange(l.LinesPerSec, 0, 1000000) || !inRange(l.KiBPerSec, 0, 1048576) {
			problem(w, 400, "Invalid Log Ingestion Budget", "value out of range")
			return
		}
		st.logs[s.s.ID] = &l
		writeJSON(w, 200, logOf(s))
	})

	workloadOf := func(s *fakeService) map[string]any {
		mode := st.workload[s.s.ID]
		if mode == "" {
			mode = "legacy"
		}
		return map[string]any{"service_id": s.s.ID, "service_name": s.s.Name, "service_type": s.s.ServiceType, "mode": mode,
			"support": map[string]any{"supported": true}}
	}
	admin := func(w http.ResponseWriter) bool {
		if st.notAdmin {
			problem(w, 403, "Forbidden", "Only an instance administrator can read the workload mode")
			return false
		}
		return true
	}
	h("GET /api/external-services/{id}/workload-mode", func(w http.ResponseWriter, r *http.Request) {
		if s := svc(w, r); s != nil && admin(w) {
			writeJSON(w, 200, workloadOf(s))
		}
	})
	h("PUT /api/external-services/{id}/workload-mode", func(w http.ResponseWriter, r *http.Request) {
		s := svc(w, r)
		if s == nil || !admin(w) {
			return
		}
		var req struct {
			Mode string `json:"mode"`
		}
		_ = decode(r, &req)
		prev := workloadOf(s)["mode"]
		st.workload[s.s.ID] = req.Mode
		writeJSON(w, 200, map[string]any{"previous_mode": prev, "workload": workloadOf(s)})
	})

	// --------------------------------------------------- flag targeting rules
	h("PUT /api/projects/{pid}/flags/{key}/environments/{env}/rules", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := f.x.flags[fmt.Sprintf("%d/%s", pathID(r, "pid"), r.PathValue("key"))]
		if !ok {
			problem(w, 404, "Feature Flag Not Found", fmt.Sprintf("Feature flag '%s' not found in project %s", r.PathValue("key"), r.PathValue("pid")))
			return
		}
		env := pathID(r, "env")
		if !f.envOf(pathID(r, "pid"), env) {
			problem(w, 404, "Environment Not Found", fmt.Sprintf("Environment %d not found in project %s", env, r.PathValue("pid")))
			return
		}
		var req struct {
			Rules    *[]client.FlagRule `json:"rules"`
			Expected *string            `json:"expected_updated_at"`
		}
		if err := decode(r, &req); err != nil || req.Rules == nil {
			problem(w, 422, "Unprocessable Entity", "missing field `rules`")
			return
		}
		for i, rule := range *req.Rules {
			if rule.Conditions == nil {
				problem(w, 422, "Unprocessable Entity", fmt.Sprintf("rules[%d].conditions: invalid type: null, expected a sequence", i))
				return
			}
			total := int64(0)
			for _, s := range rule.Serve.Split {
				total += s.Weight
			}
			if rule.Serve.Kind == "split" && total != 100 {
				problem(w, 400, "Invalid Flag Rules", fmt.Sprintf("rule %d: split weights add up to %d, not 100", i, total))
				return
			}
		}
		idx := -1
		for i, e := range fl.Environments {
			if e.EnvironmentID == env {
				idx = i
			}
		}
		if st.bumpRules && idx >= 0 { // someone else saved the row meanwhile
			st.bumpRules = false
			st.touchFlagEnv(&fl.Environments[idx])
		}
		switch {
		case idx >= 0 && (req.Expected == nil || *req.Expected != fl.Environments[idx].UpdatedAt):
			problem(w, 409, "Stale Write", fmt.Sprintf("The rules of flag '%s' in environment %d changed since they were read", fl.Key, env))
			return
		case idx < 0 && req.Expected != nil:
			problem(w, 409, "Stale Write", fmt.Sprintf("The rules of flag '%s' in environment %d changed since they were read", fl.Key, env))
			return
		case idx < 0:
			fl.Environments = append(fl.Environments, client.FeatureFlagEnvironment{EnvironmentID: env, Enabled: true, Value: []byte("null")})
			idx = len(fl.Environments) - 1
		}
		e := &fl.Environments[idx]
		// Temps stores what it decoded: optional fields left out stay out,
		// numbers come back as floats.
		b, _ := json.Marshal(*req.Rules)
		var stored []client.FlagRule
		_ = json.Unmarshal(b, &stored)
		for i := range stored {
			for j := range stored[i].Conditions {
				stored[i].Conditions[j].Value = reencode(stored[i].Conditions[j].Value)
			}
			stored[i].Serve.Value = reencode(stored[i].Serve.Value)
		}
		e.Rules = &stored
		st.touchFlagEnv(e)
		st.ruleSets++
		writeJSON(w, 200, e)
	})

	// ------------------------------------------- TLS domains and their settings
	h("POST /api/domains", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = decode(r, &req)
		name, _ := req["domain"].(string)
		method, _ := req["challenge_type"].(string)
		if method == "" {
			method = "http-01"
		}
		id := f.nextID()
		st.certDomains[id] = map[string]any{"id": id, "domain": name, "verification_method": method, "status": "pending",
			"is_wildcard": strings.HasPrefix(name, "*."), "dns_challenge_token": nil, "dns_challenge_value": nil, "last_error": nil}
		writeJSON(w, 201, st.certDomains[id])
	})
	h("GET /api/domains/{id}", func(w http.ResponseWriter, r *http.Request) {
		d, ok := st.certDomains[pathID(r, "id")]
		if !ok {
			problem(w, 404, "Domain Not Found", "Domain "+r.PathValue("id")+" not found")
			return
		}
		writeJSON(w, 200, d)
	})
	h("DELETE /api/domains/{domain}", func(w http.ResponseWriter, r *http.Request) {
		for id, d := range st.certDomains {
			if d["domain"] == r.PathValue("domain") {
				delete(st.certDomains, id)
				delete(st.certs, id)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		problem(w, 404, "Domain Not Found", "Domain "+r.PathValue("domain")+" not found")
	})
	// Like Temps: a domain without saved settings reports the defaults, with
	// the challenge of its last order.
	certOf := func(id int64) map[string]any {
		d := st.certDomains[id]
		out := map[string]any{"challenge_type": d["verification_method"], "renewal_days": 30, "alert_days": nil, "alerts_enabled": nil}
		for k, v := range st.certs[id] {
			out[k] = v
		}
		out["renewal_mode"] = "acme"
		return out
	}
	h("GET /api/domains/{id}/certificate-settings", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r, "id")
		if _, ok := st.certDomains[id]; !ok {
			problem(w, 404, "Domain Not Found", "Domain "+r.PathValue("id")+" not found")
			return
		}
		writeJSON(w, 200, certOf(id))
	})
	h("PUT /api/domains/{id}/certificate-settings", func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r, "id")
		if _, ok := st.certDomains[id]; !ok {
			problem(w, 404, "Domain Not Found", "Domain "+r.PathValue("id")+" not found")
			return
		}
		var req map[string]any
		_ = decode(r, &req)
		days, _ := req["renewal_days"].(float64)
		ct, _ := req["challenge_type"].(string)
		alert, hasAlert := req["alert_days"].(float64)
		_, sentAlerts := req["alerts_enabled"]
		_, sentAlertDays := req["alert_days"]
		if !sentAlerts || !sentAlertDays || (ct != "http-01" && ct != "dns-01") || days < 1 || days > 90 || (hasAlert && (alert < 1 || alert > 90)) {
			problem(w, 400, "Invalid Domain", "Invalid certificate settings: choose http-01 or dns-01 (wildcards require dns-01), with thresholds between 1 and 90 days")
			return
		}
		st.certs[id] = map[string]any{"challenge_type": ct, "renewal_days": days, "alert_days": req["alert_days"], "alerts_enabled": req["alerts_enabled"]}
		writeJSON(w, 200, certOf(id))
	})
}
