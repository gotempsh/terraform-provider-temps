// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func init() { moreRoutes = append(moreRoutes, (*moreFake).analyticsRoutes) }

// normalizeFilter mimics the server's normalization of a filter value.
func normalizeFilter(kind, v string) (string, bool) {
	v = strings.TrimSpace(v)
	switch kind {
	case "referrer":
		v = strings.ToLower(v)
		if _, rest, ok := strings.Cut(v, "://"); ok {
			v = rest
		}
		if i := strings.IndexAny(v, "/?#"); i >= 0 {
			v = v[:i]
		}
		v = strings.TrimPrefix(v, "www.")
		return v, strings.Contains(v, ".")
	case "asn":
		v = strings.TrimPrefix(strings.ToUpper(v), "AS")
		_, err := strconv.Atoi(v)
		return v, err == nil
	case "country":
		return strings.ToUpper(v), len(v) == 2
	}
	return v, v != ""
}

func (f *moreFake) analyticsRoutes(h moreHandler) {
	goalFields := []string{"name", "kind", "event_name", "event_filter", "page_path", "revenue_property", "revenue_currency"}
	saveGoal := func(w http.ResponseWriter, pid, id int64, b map[string]any) map[string]any {
		for gid, g := range f.goals {
			if gid != id && g["project_id"] == pid && g["name"] == strings.TrimSpace(fmt.Sprint(b["name"])) {
				problem(w, http.StatusConflict, "Conversion Goal Already Exists", fmt.Sprintf("a goal named %v already exists", b["name"]))
				return nil
			}
		}
		g := map[string]any{"id": id, "project_id": pid, "event_filter": []any{},
			"created_at": "2026-11-26T10:00:00Z", "updated_at": "2026-11-26T10:00:00Z"}
		for _, k := range goalFields {
			g[k] = b[k]
			if s, ok := b[k].(string); ok {
				g[k] = strings.TrimSpace(s)
			}
		}
		if g["event_filter"] == nil {
			g["event_filter"] = []any{}
		}
		if c, ok := g["revenue_currency"].(string); ok {
			g["revenue_currency"] = strings.ToUpper(c)
		}
		if g["kind"] == "event" && g["event_name"] == nil {
			problem(w, http.StatusBadRequest, "Invalid Conversion Goal", "an event goal needs event_name")
			return nil
		}
		f.goals[id] = g
		return g
	}
	goal := func(w http.ResponseWriter, r *http.Request) map[string]any {
		g, ok := f.goals[pathID(r, "id")]
		if !ok || fmt.Sprint(g["project_id"]) != r.PathValue("pid") {
			notFound(w, "conversion goal", r.PathValue("id"))
			return nil
		}
		return g
	}
	h("POST /api/projects/{pid}/analytics/goals", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		pid, ok := knownParent(w, r, "pid", "project", moreProjects)
		if !ok {
			return
		}
		if g := saveGoal(w, pid, f.seq+1, b); g != nil {
			f.seq++
			writeJSON(w, http.StatusCreated, g)
		}
	})
	h("GET /api/projects/{pid}/analytics/goals/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if g := goal(w, r); g != nil {
			writeJSON(w, http.StatusOK, g)
		}
	})
	h("PUT /api/projects/{pid}/analytics/goals/{id}", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		if g := goal(w, r); g != nil {
			if g := saveGoal(w, g["project_id"].(int64), g["id"].(int64), b); g != nil {
				writeJSON(w, http.StatusOK, g)
			}
		}
	})
	h("DELETE /api/projects/{pid}/analytics/goals/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if g := goal(w, r); g != nil {
			delete(f.goals, g["id"].(int64))
			w.WriteHeader(http.StatusNoContent)
		}
	})

	h("POST /api/projects/{pid}/analytics/traffic-filters", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		pid, ok := knownParent(w, r, "pid", "project", moreProjects)
		if !ok {
			return
		}
		kind, _ := b["kind"].(string)
		v, valid := normalizeFilter(kind, fmt.Sprint(b["value"]))
		if !valid {
			problem(w, http.StatusBadRequest, "Invalid Traffic Filter", fmt.Sprintf("%q is not a valid %s value", b["value"], kind))
			return
		}
		for _, x := range f.filters {
			if x["project_id"] == pid && x["kind"] == kind && x["value"] == v {
				problem(w, http.StatusConflict, "Duplicate Traffic Filter", "the project already filters "+v)
				return
			}
		}
		flt := map[string]any{"id": f.next(), "project_id": pid, "kind": kind, "value": v, "note": b["note"],
			"created_by": 1, "created_at": "2026-11-26T10:00:00Z"}
		f.filters[flt["id"].(int64)] = flt
		writeJSON(w, http.StatusCreated, flt)
	})
	h("GET /api/projects/{pid}/analytics/traffic-filters", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		pid, ok := knownParent(w, r, "pid", "project", moreProjects)
		if !ok {
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
		if page < 1 {
			page = 1
		}
		if size < 1 || size > 100 {
			size = 20
		}
		var all []any
		ids := sortedIDs(f.filters)
		for i := len(ids) - 1; i >= 0; i-- { // newest first
			if flt := f.filters[ids[i]]; flt["project_id"] == pid {
				all = append(all, flt)
			}
		}
		lo, hi := min((page-1)*size, len(all)), min(page*size, len(all))
		writeJSON(w, http.StatusOK, map[string]any{"filters": nonNil(all[lo:hi]), "total": len(all), "page": page, "page_size": size,
			"max_filters_per_kind": 500})
	})
	h("DELETE /api/projects/{pid}/analytics/traffic-filters/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		flt, ok := f.filters[pathID(r, "id")]
		if !ok || fmt.Sprint(flt["project_id"]) != r.PathValue("pid") {
			notFound(w, "traffic filter", r.PathValue("id"))
			return
		}
		delete(f.filters, pathID(r, "id"))
		w.WriteHeader(http.StatusNoContent)
	})

	settings := func(pid int64) map[string]any {
		s, ok := f.settings[pid]
		if !ok {
			s = map[string]any{"project_id": pid, "exclude_datacenter_traffic": false, "exclude_spam_referrers": true,
				"spam_referrer_list_size": 1200, "updated_at": nil,
				"datacenter_detection": map[string]any{"available": true, "asn_database_built_at": "2026-11-01T00:00:00Z"}}
			f.settings[pid] = s
		}
		return s
	}
	h("GET /api/projects/{pid}/analytics/settings", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if pid, ok := knownParent(w, r, "pid", "project", moreProjects); ok {
			writeJSON(w, http.StatusOK, settings(pid))
		}
	})
	h("PATCH /api/projects/{pid}/analytics/settings", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		pid, ok := knownParent(w, r, "pid", "project", moreProjects)
		if !ok {
			return
		}
		s := settings(pid)
		for _, k := range []string{"exclude_datacenter_traffic", "exclude_spam_referrers"} {
			if v, ok := b[k]; ok && v != nil {
				s[k] = v
			}
		}
		s["updated_at"] = "2026-11-26T10:00:00Z"
		writeJSON(w, http.StatusOK, s)
	})
}
