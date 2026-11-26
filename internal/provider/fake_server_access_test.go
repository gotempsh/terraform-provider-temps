// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"fmt"
	"net/http"
)

func init() { moreRoutes = append(moreRoutes, (*moreFake).accessRoutes) }

func (f *moreFake) accessRoutes(h moreHandler) {
	createService := func(w http.ResponseWriter, project any, b map[string]any) {
		if _, ok := b["expected_version"]; ok {
			problem(w, http.StatusBadRequest, "Bad Request", "expected_version is forbidden for creation")
			return
		}
		id := fmt.Sprintf("svc_%d", f.next())
		s := map[string]any{"id": id, "project_id": project, "name": b["name"], "policy": b["policy"],
			"max_ttl_seconds": b["max_ttl_seconds"], "enabled": b["enabled"], "has_credential": b["credential"] != nil, "version": 1}
		f.services[id] = s
		writeJSON(w, http.StatusCreated, s)
	}
	h("POST /api/access-services", func(w http.ResponseWriter, _ *http.Request, b map[string]any) {
		createService(w, nil, b)
	})
	h("POST /api/projects/{pid}/access-services", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		if pid, ok := knownParent(w, r, "pid", "project", moreProjects); ok {
			createService(w, pid, b)
		}
	})
	h("GET /api/access-services/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		s, ok := f.services[r.PathValue("id")]
		if !ok {
			notFound(w, "service", r.PathValue("id"))
			return
		}
		writeJSON(w, http.StatusOK, s)
	})
	h("PUT /api/access-services/{id}", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		s, ok := f.services[r.PathValue("id")]
		if !ok {
			notFound(w, "service", r.PathValue("id"))
			return
		}
		if _, ok := b["vault_id"]; ok {
			problem(w, http.StatusBadRequest, "Invalid request", "vault_id: only accepted when creating a service")
			return
		}
		if fmt.Sprint(b["expected_version"]) != fmt.Sprint(s["version"]) {
			problem(w, http.StatusConflict, "Conflict", "version conflict")
			return
		}
		for _, k := range []string{"name", "policy", "max_ttl_seconds", "enabled"} {
			s[k] = b[k]
		}
		if b["credential"] != nil {
			s["has_credential"] = true
		}
		v, _ := numberInt(s["version"])
		s["version"] = v + 1
		writeJSON(w, http.StatusOK, s)
	})
	h("DELETE /api/access-services/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if _, ok := f.services[r.PathValue("id")]; !ok {
			notFound(w, "service", r.PathValue("id"))
			return
		}
		delete(f.services, r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})

	createVault := func(w http.ResponseWriter, project any, b map[string]any) {
		id := fmt.Sprintf("vault_%d", f.next())
		v := map[string]any{"id": id, "project_id": project, "name": b["name"], "service_ids": b["service_ids"], "version": 1}
		f.vaults[id] = v
		if project != nil {
			f.links = append(f.links, map[string]any{"vault_id": id, "project_id": project})
		}
		writeJSON(w, http.StatusCreated, v)
	}
	h("POST /api/access-vaults", func(w http.ResponseWriter, _ *http.Request, b map[string]any) {
		createVault(w, nil, b)
	})
	h("POST /api/projects/{pid}/access-vaults", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		if pid, ok := knownParent(w, r, "pid", "project", moreProjects); ok {
			createVault(w, pid, b)
		}
	})
	h("GET /api/access-vaults/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		v, ok := f.vaults[r.PathValue("id")]
		if !ok {
			notFound(w, "vault", r.PathValue("id"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"vault": v, "services": []any{}})
	})
	h("PUT /api/access-vaults/{id}", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		v, ok := f.vaults[r.PathValue("id")]
		if !ok {
			notFound(w, "vault", r.PathValue("id"))
			return
		}
		if fmt.Sprint(b["expected_version"]) != fmt.Sprint(v["version"]) {
			problem(w, http.StatusConflict, "Conflict", "version conflict")
			return
		}
		v["name"], v["service_ids"] = b["name"], b["service_ids"]
		n, _ := numberInt(v["version"])
		v["version"] = n + 1
		writeJSON(w, http.StatusOK, v)
	})
	h("DELETE /api/access-vaults/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if _, ok := f.vaults[r.PathValue("id")]; !ok {
			notFound(w, "vault", r.PathValue("id"))
			return
		}
		delete(f.vaults, r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})
}
