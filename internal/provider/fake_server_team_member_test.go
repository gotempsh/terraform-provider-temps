// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"fmt"
	"net/http"
)

func init() { moreRoutes = append(moreRoutes, (*moreFake).teamRoutes) }

func (f *moreFake) teamRoutes(h moreHandler) {
	h("POST /api/teams/{team}/members", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		team, ok := knownParent(w, r, "team", "team", moreTeams)
		if !ok {
			return
		}
		uid, _ := numberInt(b["user_id"])
		u, known := moreUsers[uid]
		if !known {
			notFound(w, "user", uid)
			return
		}
		key := fmt.Sprintf("%d/%d", team, uid)
		if _, dup := f.members[key]; dup {
			problem(w, http.StatusConflict, "Conflict", fmt.Sprintf("user %d is already a member of team %d", uid, team))
			return
		}
		m := map[string]any{"id": f.next(), "team_id": team, "user_id": uid, "role": b["role"], "added_by": 1,
			"user_email": u[0], "user_name": u[1], "created_at": "2026-11-26T10:00:00Z", "updated_at": "2026-11-26T10:00:00Z"}
		f.members[key] = m
		writeJSON(w, http.StatusCreated, m)
	})
	member := func(w http.ResponseWriter, r *http.Request) (string, map[string]any) {
		key := r.PathValue("team") + "/" + r.PathValue("user")
		m, ok := f.members[key]
		if !ok {
			notFound(w, "team member", key)
		}
		return key, m
	}
	h("GET /api/teams/{team}/members/{user}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if _, m := member(w, r); m != nil {
			writeJSON(w, http.StatusOK, m)
		}
	})
	h("PATCH /api/teams/{team}/members/{user}", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		if _, m := member(w, r); m != nil {
			if _, ok := b["user_id"]; ok {
				problem(w, http.StatusBadRequest, "Bad Request", "unknown field user_id")
				return
			}
			m["role"] = b["role"]
			writeJSON(w, http.StatusOK, m)
		}
	})
	h("DELETE /api/teams/{team}/members/{user}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if key, m := member(w, r); m != nil {
			delete(f.members, key)
			w.WriteHeader(http.StatusNoContent)
		}
	})
}
