// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"fmt"
	"net/http"
	"slices"
)

func init() {
	moreRoutes = append(moreRoutes, (*moreFake).ruleRoutes, (*moreFake).otelRoutes)
}

func newMetricRule(id int64, b map[string]any) map[string]any {
	rule := map[string]any{"id": id, "name": b["name"], "metric_name": b["metric_name"], "threshold": b["threshold"],
		"comparator": b["comparator"], "severity": b["severity"], "for_duration_secs": 0, "enabled": true,
		"silenced_until": nil, "deployment_id": nil, "service_id": nil, "node_id": nil}
	for _, k := range []string{"for_duration_secs", "enabled"} {
		if v, ok := b[k]; ok {
			rule[k] = v
		}
	}
	return rule
}

func patchRule(rule, b map[string]any) {
	for _, k := range []string{"name", "metric_name", "threshold", "comparator", "severity", "for_duration_secs", "enabled"} {
		if v, ok := b[k]; ok && v != nil {
			rule[k] = v
		}
	}
}

func (f *moreFake) ruleRoutes(h moreHandler) {
	h("POST /api/nodes/{node}/metrics/alert-rules", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		node, ok := knownParent(w, r, "node", "node", moreNodes)
		if !ok {
			return
		}
		for _, x := range f.nodeRules {
			if x["node_id"] == node && x["metric_name"] == b["metric_name"] {
				problem(w, http.StatusConflict, "Alert Rule Already Exists", fmt.Sprintf("Node %d already has an alert rule for '%v' (rule %v)", node, b["metric_name"], x["id"]))
				return
			}
		}
		rule := newMetricRule(f.next(), b)
		rule["node_id"] = node
		f.nodeRules[rule["id"].(int64)] = rule
		writeJSON(w, http.StatusCreated, rule)
	})
	nodeRule := func(w http.ResponseWriter, r *http.Request) map[string]any {
		rule, ok := f.nodeRules[pathID(r, "rule")]
		if !ok || fmt.Sprint(rule["node_id"]) != r.PathValue("node") {
			notFound(w, "alert rule", r.PathValue("rule"))
			return nil
		}
		return rule
	}
	h("GET /api/nodes/{node}/metrics/alert-rules/{rule}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if rule := nodeRule(w, r); rule != nil {
			writeJSON(w, http.StatusOK, rule)
		}
	})
	h("PATCH /api/nodes/{node}/metrics/alert-rules/{rule}", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		if rule := nodeRule(w, r); rule != nil {
			patchRule(rule, b)
			writeJSON(w, http.StatusOK, rule)
		}
	})
	h("DELETE /api/nodes/{node}/metrics/alert-rules/{rule}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		rule := nodeRule(w, r)
		if rule == nil {
			return
		}
		if r.PathValue("node") == "0" {
			problem(w, http.StatusConflict, "Rule Protected", "Alert rule belongs to the control plane; disable it instead of deleting it")
			return
		}
		if slices.Contains(diskHealthNodeMetrics, fmt.Sprint(rule["metric_name"])) {
			problem(w, http.StatusConflict, "Rule Protected", "the default disk-health rule is re-created; disable it instead of deleting it")
			return
		}
		delete(f.nodeRules, rule["id"].(int64))
		w.WriteHeader(http.StatusNoContent)
	})

	h("POST /api/external-services/{svc}/metrics/alert-rules", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		svc, ok := knownParent(w, r, "svc", "external service", moreServices)
		if !ok {
			return
		}
		rule := newMetricRule(f.next(), b)
		rule["service_id"] = svc
		f.svcRules[rule["id"].(int64)] = rule
		writeJSON(w, http.StatusCreated, rule)
	})
	h("GET /api/external-services/{svc}/metrics/alert-rules", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		svc, ok := knownParent(w, r, "svc", "external service", moreServices)
		if !ok {
			return
		}
		out := []any{}
		for _, id := range sortedIDs(f.svcRules) {
			if f.svcRules[id]["service_id"] == svc {
				out = append(out, f.svcRules[id])
			}
		}
		writeJSON(w, http.StatusOK, out)
	})
	svcRule := func(w http.ResponseWriter, r *http.Request) map[string]any {
		rule, ok := f.svcRules[pathID(r, "rule")]
		if !ok || fmt.Sprint(rule["service_id"]) != r.PathValue("svc") {
			problem(w, http.StatusNotFound, "Not Found", "Alert rule not found")
			return nil
		}
		return rule
	}
	h("PUT /api/external-services/{svc}/metrics/alert-rules/{rule}", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		if rule := svcRule(w, r); rule != nil {
			patchRule(rule, b)
			writeJSON(w, http.StatusOK, rule)
		}
	})
	h("DELETE /api/external-services/{svc}/metrics/alert-rules/{rule}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if rule := svcRule(w, r); rule != nil {
			delete(f.svcRules, rule["id"].(int64))
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

// scoped finds an object by ID in the project named by ?project_id, as the
// real handlers do; a missing or other project answers 404.
func scoped(w http.ResponseWriter, r *http.Request, store map[int64]map[string]any, what string) map[string]any {
	obj, ok := store[pathID(r, "id")]
	if !ok || r.URL.Query().Get("project_id") == "" || fmt.Sprint(obj["project_id"]) != r.URL.Query().Get("project_id") {
		notFound(w, what, r.PathValue("id"))
		return nil
	}
	return obj
}

func bodyProject(w http.ResponseWriter, b map[string]any) (int64, bool) {
	pid, _ := numberInt(b["project_id"])
	if !slices.Contains(moreProjects, pid) {
		notFound(w, "project", pid)
		return 0, false
	}
	return pid, true
}

func (f *moreFake) otelRoutes(h moreHandler) {
	otelFields := []string{"name", "metric_name", "aggregation", "source", "detection_config", "window_secs",
		"for_duration_secs", "severity", "enabled", "label_filters", "group_by", "dynamic_alerts", "max_series",
		"grouped_notification_threshold"}
	normalize := func(rule map[string]any) {
		if src, _ := rule["source"].(map[string]any); src["kind"] == "logs" {
			rule["aggregation"] = logAlertAggregation
			rule["metric_name"] = logAlertMetricName
		}
		dc, _ := rule["detection_config"].(map[string]any)
		rule["detection_kind"] = dc["kind"]
	}
	h("POST /api/otel/alerts", func(w http.ResponseWriter, _ *http.Request, b map[string]any) {
		pid, ok := bodyProject(w, b)
		if !ok {
			return
		}
		rule := map[string]any{"id": f.next(), "project_id": pid, "metric_name": "", "source": map[string]any{"kind": "metric"},
			"label_filters": []any{}, "group_by": []any{}, "dynamic_alerts": false, "max_series": 20,
			"grouped_notification_threshold": 5, "last_state": "unknown", "last_value": nil, "series_states": map[string]any{},
			"firing_series": []any{}, "created_at": "2026-11-26T10:00:00Z", "updated_at": "2026-11-26T10:00:00Z"}
		for _, k := range otelFields {
			if v, ok := b[k]; ok && v != nil {
				rule[k] = v
			}
		}
		normalize(rule)
		f.otelRules[rule["id"].(int64)] = rule
		writeJSON(w, http.StatusCreated, rule)
	})
	h("GET /api/otel/alerts/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if rule := scoped(w, r, f.otelRules, "alert rule"); rule != nil {
			writeJSON(w, http.StatusOK, rule)
		}
	})
	h("PATCH /api/otel/alerts/{id}", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		rule := scoped(w, r, f.otelRules, "alert rule")
		if rule == nil {
			return
		}
		if _, ok := b["project_id"]; ok {
			problem(w, http.StatusBadRequest, "Bad Request", "project_id cannot be changed")
			return
		}
		for _, k := range otelFields {
			if v, ok := b[k]; ok && v != nil {
				rule[k] = v
			}
		}
		normalize(rule)
		writeJSON(w, http.StatusOK, rule)
	})
	h("DELETE /api/otel/alerts/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if rule := scoped(w, r, f.otelRules, "alert rule"); rule != nil {
			delete(f.otelRules, rule["id"].(int64))
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// Dashboards: Temps decodes the layout into typed sections and tiles, so
	// tiles come back with every default filled in and unknown keys dropped.
	keep := func(obj map[string]any, keys []string) {
		for k := range obj {
			if !slices.Contains(keys, k) {
				delete(obj, k)
			}
		}
	}
	fillLayout := func(layout any) any {
		l := copyDoc(map[string]any{"l": layout})["l"]
		keep(asObject(l), dashboardLayoutKeys)
		sections, _ := asObject(l)["sections"].([]any)
		for _, s := range sections {
			keep(asObject(s), dashboardSectionKeys)
			tiles, _ := asObject(s)["tiles"].([]any)
			for _, t := range tiles {
				tile := asObject(t)
				keep(tile, dashboardTileKeys)
				for k, d := range map[string]any{"title": nil, "label_filters": []any{}, "group_by": []any{}, "source": map[string]any{"kind": "metric"}} {
					if _, ok := tile[k]; !ok {
						tile[k] = d
					}
				}
			}
		}
		return l
	}
	h("POST /api/otel/dashboards", func(w http.ResponseWriter, _ *http.Request, b map[string]any) {
		pid, ok := bodyProject(w, b)
		if !ok {
			return
		}
		if _, ok := asObject(b["layout"])["sections"]; !ok {
			problem(w, http.StatusUnprocessableEntity, "Unprocessable Entity", "layout: missing field `sections`")
			return
		}
		d := map[string]any{"id": f.next(), "project_id": pid, "name": b["name"], "layout": fillLayout(b["layout"]),
			"created_at": "2026-11-26T10:00:00Z", "updated_at": "2026-11-26T10:00:00Z"}
		f.dashes[d["id"].(int64)] = d
		writeJSON(w, http.StatusCreated, d)
	})
	h("GET /api/otel/dashboards/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if d := scoped(w, r, f.dashes, "dashboard"); d != nil {
			writeJSON(w, http.StatusOK, d)
		}
	})
	h("PATCH /api/otel/dashboards/{id}", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		d := scoped(w, r, f.dashes, "dashboard")
		if d == nil {
			return
		}
		if n, ok := b["name"]; ok && n != nil {
			d["name"] = n
		}
		if l, ok := b["layout"]; ok && l != nil {
			d["layout"] = fillLayout(l)
		}
		writeJSON(w, http.StatusOK, d)
	})
	h("DELETE /api/otel/dashboards/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if d := scoped(w, r, f.dashes, "dashboard"); d != nil {
			delete(f.dashes, d["id"].(int64))
			w.WriteHeader(http.StatusNoContent)
		}
	})
}
