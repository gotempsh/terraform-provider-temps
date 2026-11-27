// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"fmt"
	"net/http"
	"slices"
)

func init() { moreRoutes = append(moreRoutes, (*moreFake).funnelRoutes) }

// funnelRoutes serve a project's funnels. Like Temps from 2026-11-25 on,
// writes return the funnel and the list is paged, newest first; released
// servers answered writes with a message and the list with a bare array
// (see funnelList).
func (f *moreFake) funnelRoutes(h moreHandler) {
	save := func(id, pid int64, b map[string]any) map[string]any {
		fn := map[string]any{"id": id, "project_id": pid, "name": b["name"], "description": b["description"],
			"steps": b["steps"], "is_active": true, "created_at": "2026-11-26T10:00:00Z", "updated_at": "2026-11-26T10:00:00Z"}
		f.funnels[id] = fn
		return fn
	}
	funnel := func(w http.ResponseWriter, r *http.Request) map[string]any {
		fn, ok := f.funnels[pathID(r, "id")]
		if !ok || fmt.Sprint(fn["project_id"]) != r.PathValue("pid") {
			notFound(w, "funnel", r.PathValue("id"))
			return nil
		}
		return fn
	}
	legacy := func() bool { return f.funnelList == "array" }
	h("POST /api/projects/{pid}/funnels", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		pid, ok := knownParent(w, r, "pid", "project", moreProjects)
		if !ok {
			return
		}
		fn := save(f.next(), pid, b)
		if legacy() {
			writeJSON(w, http.StatusCreated, map[string]any{"funnel_id": fn["id"], "message": "Funnel created successfully"})
			return
		}
		writeJSON(w, http.StatusCreated, fn)
	})
	h("GET /api/projects/{pid}/funnels", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if _, ok := knownParent(w, r, "pid", "project", moreProjects); !ok {
			return
		}
		if f.funnelList != "" && !legacy() {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(f.funnelList))
			return
		}
		ids := sortedIDs(f.funnels)
		slices.Reverse(ids)
		out := []map[string]any{}
		for _, id := range ids {
			if fn := f.funnels[id]; fmt.Sprint(fn["project_id"]) == r.PathValue("pid") {
				item := copyDoc(fn)
				if legacy() {
					delete(item, "steps") // released servers did not return them
				}
				out = append(out, item)
			}
		}
		writePage(w, r, out, legacy())
	})
	h("PUT /api/projects/{pid}/funnels/{id}", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		fn := funnel(w, r)
		if fn == nil {
			return
		}
		fn = save(pathID(r, "id"), pathID(r, "pid"), b)
		if legacy() {
			writeJSON(w, http.StatusOK, map[string]any{"message": "Funnel updated successfully"})
			return
		}
		writeJSON(w, http.StatusOK, fn)
	})
	h("DELETE /api/projects/{pid}/funnels/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if fn := funnel(w, r); fn != nil {
			delete(f.funnels, pathID(r, "id"))
			w.WriteHeader(http.StatusNoContent)
		}
	})
}
