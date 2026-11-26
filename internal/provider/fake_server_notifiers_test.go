// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"fmt"
	"net/http"
	"strings"
)

func init() { moreRoutes = append(moreRoutes, (*moreFake).notificationRoutes) }

// notifierSecrets are the config keys the real server masks for these kinds.
var notifierSecrets = []string{"webhook_url", "password", "bot_token", "api_token"}

// notifierRequired are the config keys each kind's request must carry.
var notifierRequired = map[string][]string{
	"slack":      {"webhook_url"},
	"email":      {"smtp_host", "smtp_port", "username", "password", "from_address", "to_addresses"},
	"telegram":   {"bot_token", "chat_id"},
	"cloudflare": {"account_id", "api_token", "from_address", "to_addresses"},
}

func maskNotifier(p map[string]any) map[string]any {
	out := copyDoc(p)
	cfg, _ := out["config"].(map[string]any)
	for _, k := range notifierSecrets {
		if v, ok := cfg[k]; ok && v != nil {
			cfg[k] = "***"
		}
	}
	return out
}

// storedConfig builds the config the real handler stores: every key of the
// kind, defaults filled in.
func storedConfig(kind string, in map[string]any) (map[string]any, string) {
	for _, k := range notifierRequired[kind] {
		if _, ok := in[k]; !ok {
			return nil, "missing field `" + k + "`"
		}
	}
	cfg := map[string]any{}
	switch kind {
	case "slack":
		cfg["webhook_url"], cfg["channel"] = in["webhook_url"], in["channel"]
	case "email":
		for _, k := range []string{"smtp_host", "smtp_port", "username", "password", "from_name", "from_address", "to_addresses"} {
			cfg[k] = in[k]
		}
		cfg["tls_mode"], cfg["starttls_required"], cfg["accept_invalid_certs"] = "Starttls", true, false
		for _, k := range []string{"tls_mode", "starttls_required", "accept_invalid_certs"} {
			if v, ok := in[k]; ok {
				cfg[k] = v
			}
		}
	case "telegram":
		tok, _ := in["bot_token"].(string)
		if tok != "***" && !strings.Contains(tok, ":") {
			return nil, "bot token must look like '123456789:AA…' as issued by @BotFather"
		}
		chat, _ := in["chat_id"].(string)
		cfg["bot_token"], cfg["chat_id"], cfg["message_thread_id"] = strings.TrimSpace(tok), strings.TrimSpace(chat), in["message_thread_id"]
	case "cloudflare":
		for _, k := range []string{"account_id", "api_token", "from_address", "from_name", "to_addresses"} {
			cfg[k] = in[k]
		}
	}
	return cfg, ""
}

func (f *moreFake) notificationRoutes(h moreHandler) {
	for kind := range notifierRequired {
		h("POST /api/notification-providers/"+kind, func(w http.ResponseWriter, _ *http.Request, b map[string]any) {
			in, _ := b["config"].(map[string]any)
			cfg, bad := storedConfig(kind, in)
			if bad != "" {
				problem(w, http.StatusBadRequest, "Invalid configuration", bad)
				return
			}
			for _, k := range notifierSecrets {
				if cfg[k] == "***" {
					problem(w, http.StatusBadRequest, "Invalid configuration", "masked value at '"+k+"' has no existing value to preserve")
					return
				}
			}
			id := f.next()
			enabled := true
			if e, ok := b["enabled"].(bool); ok {
				enabled = e
			}
			p := map[string]any{"id": id, "name": b["name"], "provider_type": kind, "config": cfg, "enabled": enabled,
				"created_at": "2026-11-26T10:00:00Z", "updated_at": "2026-11-26T10:00:00Z"}
			f.notifiers[id] = p
			writeJSON(w, http.StatusCreated, maskNotifier(p))
		})
		h("PUT /api/notification-providers/"+kind+"/{id}", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
			p, ok := f.notifiers[pathID(r, "id")]
			if !ok {
				notFound(w, "notification provider", r.PathValue("id"))
				return
			}
			// Only the Telegram handler checks the stored type; the others
			// overwrite whatever they are given.
			if kind == "telegram" && p["provider_type"] != kind {
				problem(w, http.StatusBadRequest, "Provider type mismatch", fmt.Sprintf("provider is %v, not telegram", p["provider_type"]))
				return
			}
			in, _ := b["config"].(map[string]any)
			cfg, bad := storedConfig(kind, in)
			if bad != "" {
				problem(w, http.StatusBadRequest, "Invalid configuration", bad)
				return
			}
			old, _ := p["config"].(map[string]any)
			if kind == "email" && cfg["smtp_host"] != old["smtp_host"] && cfg["password"] == "***" {
				problem(w, http.StatusBadRequest, "Invalid configuration", "smtp_host changed while password is masked")
				return
			}
			for _, k := range notifierSecrets {
				if cfg[k] == "***" {
					cfg[k] = old[k]
				}
			}
			p["config"], p["provider_type"] = cfg, kind
			if n, ok := b["name"]; ok && n != nil {
				p["name"] = n
			}
			if e, ok := b["enabled"].(bool); ok {
				p["enabled"] = e
			}
			writeJSON(w, http.StatusOK, maskNotifier(p))
		})
	}
	h("GET /api/notification-providers/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		p, ok := f.notifiers[pathID(r, "id")]
		if !ok {
			notFound(w, "notification provider", r.PathValue("id"))
			return
		}
		writeJSON(w, http.StatusOK, maskNotifier(p))
	})
	h("DELETE /api/notification-providers/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if _, ok := f.notifiers[pathID(r, "id")]; !ok {
			notFound(w, "notification provider", r.PathValue("id"))
			return
		}
		delete(f.notifiers, pathID(r, "id"))
		w.WriteHeader(http.StatusNoContent)
	})
}
