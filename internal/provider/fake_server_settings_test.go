// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"fmt"
	"net/http"

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
	// old makes these endpoints answer like a server that predates them: a
	// bare 404 without problem details.
	old bool

	certDomains map[int64]map[string]any // domain ID -> domain document
	certs       map[int64]map[string]any // domain ID -> stored settings
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
		route(pattern, func(w http.ResponseWriter, r *http.Request) {
			if st.old {
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

}
