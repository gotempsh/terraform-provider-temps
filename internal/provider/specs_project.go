// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Per-project and per-environment settings: domains, ingress, runtime
// health, internal access, Shield and HTTP checks.

var envPerms = []string{"environments:read", "environments:write"}

var environmentDomainSpec = &gspec{
	Name: "environment_domain",
	Desc: "A domain an environment answers on. Point its DNS at Temps; Temps obtains the certificate. " +
		"Use `temps_custom_domain` for domains with redirects or per-domain settings.",
	Perms:   envPerms,
	Parents: []string{"project_id", "environment_id"},
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "environment_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the environment."},
		{Name: "domain", Kind: kString, Required: true, Desc: "Domain name, for example `staging.example.com`."},
		{Name: "is_primary", Kind: kBool, Optional: true, Default: false, Read: "-",
			Desc: "Make it the environment's primary domain."},
		{Name: "url", Kind: kString, Computed: true, Desc: "URL the environment serves on this domain."},
	},
	Create: gop{Method: http.MethodPost, Path: "/projects/{project_id}/environments/{environment_id}/domains"},
	Read:   gop{Path: "/projects/{project_id}/environments/{environment_id}/domains"},
	List:   true,
	Delete: gop{Method: http.MethodDelete, Path: "/projects/{project_id}/environments/{environment_id}/domains/{id}"},
}

var environmentIngressSpec = &gspec{
	Name: "environment_ingress",
	Desc: "How the Temps proxy admits traffic to an environment: `legacy` keeps the old behaviour, `shadow` " +
		"evaluates the new rules without enforcing them, `enforce` applies them; `require_login` puts the " +
		"environment behind Temps sign-in.",
	Perms:     envPerms,
	Parents:   []string{"project_id", "environment_id"},
	Singleton: true,
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "environment_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the environment."},
		{Name: "mode", Kind: kString, Optional: true, OneOf: []string{"legacy", "shadow", "enforce"}, Desc: "Ingress mode."},
		{Name: "require_login", Kind: kBool, Optional: true, Desc: "Require visitors to sign in to Temps."},
		{Name: "login_enforced", Kind: kBool, Computed: true, Volatile: true, Desc: "Whether sign-in is enforced now."},
		{Name: "login_permission", Kind: kString, Computed: true, Desc: "Permission a visitor needs once signed in."},
	},
	Create:     gop{Method: http.MethodPut, Path: "/projects/{project_id}/environments/{environment_id}/ingress"},
	Read:       gop{Path: "/projects/{project_id}/environments/{environment_id}/ingress"},
	Update:     gop{Method: http.MethodPut, Path: "/projects/{project_id}/environments/{environment_id}/ingress"},
	DeleteNote: "Destroying the resource leaves the settings as they are; it only stops managing them.",
}

var runtimeHealthSpec = &gspec{
	Name: "environment_runtime_health",
	Desc: "Whether Temps watches an environment's running replicas and how many must be healthy. " +
		"Destroying the resource turns watching off.",
	Perms:     envPerms,
	Parents:   []string{"project_id", "environment_id"},
	Singleton: true,
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "environment_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the environment."},
		{Name: "enabled", Kind: kBool, Required: true, Read: "policy.enabled", Desc: "Watch the replicas."},
		{Name: "paused", Kind: kBool, Optional: true, Default: false, Read: "policy.paused",
			Desc: "Keep the policy but stop acting on it, for example during maintenance."},
		{Name: "required_healthy", Kind: kInt, Clearable: true, Read: "policy.required_healthy",
			Desc: "Replicas that must be healthy; unset means all of them."},
		{Name: "state", Kind: kString, Computed: true, Volatile: true, Desc: "Current health state."},
		{Name: "reason", Kind: kString, Computed: true, Volatile: true, Desc: "Why the environment is in that state."},
	},
	Create:         gop{Method: http.MethodPut, Path: "/projects/{project_id}/environments/{environment_id}/runtime-health"},
	Read:           gop{Path: "/projects/{project_id}/environments/{environment_id}/runtime-health"},
	Update:         gop{Method: http.MethodPut, Path: "/projects/{project_id}/environments/{environment_id}/runtime-health"},
	Delete:         gop{Method: http.MethodPut, Path: "/projects/{project_id}/environments/{environment_id}/runtime-health", Body: map[string]any{"enabled": false, "paused": false, "required_healthy": nil}},
	ReadAfterWrite: true,
	BodyHook: func(body map[string]any, _ gvals, _ bool) {
		if _, ok := body["required_healthy"]; !ok {
			body["required_healthy"] = nil
		}
	},
}

var internalAccessSpec = &gspec{
	Name: "project_internal_access",
	Desc: "Lets another project (or one of its environments) call this project's services over the internal " +
		"network. Destroying it revokes the grant.",
	Perms:   []string{"projects:read", "projects:write"},
	Parents: []string{"project_id"},
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project being called."},
		{Name: "caller_project_id", Kind: kInt, Required: true, Desc: "ID of the project allowed to call it."},
		{Name: "caller_environment_id", Kind: kInt, Optional: true,
			Desc: "Limit the grant to one environment of the caller; unset means all of them."},
		{Name: "target_environment_id", Kind: kInt, Optional: true,
			Desc: "Limit the grant to one environment of this project; unset means all of them."},
		{Name: "description", Kind: kString, Optional: true, Desc: "Why the access exists."},
	},
	Create: gop{Method: http.MethodPost, Path: "/projects/{project_id}/internal-access"},
	Read:   gop{Path: "/projects/{project_id}/internal-access"},
	List:   true, ListKey: "items", Paged: true,
	Delete: gop{Method: http.MethodDelete, Path: "/projects/{project_id}/internal-access/{id}"},
}

func shieldEntriesPath(v gvals) string {
	if p, ok := v["project_id"].(types.Int64); ok && known(p) {
		return "/projects/" + scalarString(p) + "/shield/entries"
	}
	return "/settings/shield/entries"
}

var shieldEntrySpec = &gspec{
	Name: "shield_entry",
	Desc: "A Shield rule: a verdict for clients from a network or with a TLS fingerprint (JA4). Without " +
		"`project_id` it applies to the whole platform. Set exactly one of `network` and `ja4`.",
	Perms:           []string{"projects:read", "projects:write", "settings:write"},
	Parents:         []string{"project_id"},
	OptionalParents: true,
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Optional: true, CreateOnly: true, Path: true,
			Desc: "Project the entry applies to; unset for a platform-wide entry."},
		{Name: "network", Kind: kString, Optional: true, CreateOnly: true, Desc: "CIDR network, for example `198.51.100.0/24`."},
		{Name: "ja4", Kind: kString, Optional: true, CreateOnly: true, Desc: "JA4 TLS client fingerprint."},
		{Name: "verdict", Kind: kString, Required: true, OneOf: []string{"allow", "log", "challenge", "block"},
			Desc: "What Shield does with matching clients."},
		{Name: "note", Kind: kString, Clearable: true, Desc: "Why the entry exists."},
		{Name: "expires_at", Kind: kString, Clearable: true, Desc: "RFC 3339 time the entry stops applying."},
		{Name: "scope", Kind: kString, Computed: true, Desc: "`platform` or `project`."},
	},
	Create: gop{Method: http.MethodPost, PathFn: func(v gvals, _ string) string { return shieldEntriesPath(v) }},
	Read:   gop{PathFn: func(v gvals, _ string) string { return shieldEntriesPath(v) }},
	List:   true,
	Update: gop{Method: http.MethodPut, PathFn: func(v gvals, id string) string { return shieldEntriesPath(v) + "/" + id }},
	Delete: gop{Method: http.MethodDelete, PathFn: func(v gvals, id string) string { return shieldEntriesPath(v) + "/" + id }},
}

var shieldTokenSpec = &gspec{
	Name: "shield_token",
	Desc: "A token that lets automated clients (CI, monitoring) bypass Shield challenges for an environment, or " +
		"the whole project with `project_wide`. The secret is only available when the token is created.",
	Perms:   []string{"projects:read", "projects:write"},
	Parents: []string{"project_id", "environment_id"},
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "environment_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the environment."},
		{Name: "name", Kind: kString, Required: true, Desc: "Name of the token."},
		{Name: "project_wide", Kind: kBool, Optional: true, Default: false, Read: "-",
			Desc: "Accept the token in every environment of the project."},
		{Name: "secret", Kind: kString, Computed: true, Sensitive: true,
			Desc: "Token value. Only known when Terraform created the token; an imported token has none."},
		{Name: "scope", Kind: kString, Computed: true, Desc: "`environment` or `project`."},
	},
	Create: gop{Method: http.MethodPost, Path: "/projects/{project_id}/environments/{environment_id}/shield/tokens"},
	Read:   gop{Path: "/projects/{project_id}/environments/{environment_id}/shield/tokens"},
	List:   true,
	Delete: gop{Method: http.MethodDelete, Path: "/projects/{project_id}/environments/{environment_id}/shield/tokens/{id}"},
}

var shieldSettingsSpec = &gspec{
	Name:      "shield_settings",
	Desc:      "Platform-wide Shield settings.",
	Perms:     []string{"settings:read", "settings:write"},
	Singleton: true,
	Fields: []gfield{
		{Name: "share_observations", Kind: kBool, Required: true,
			Desc: "Share anonymised abuse observations to improve shared blocklists."},
	},
	Create:     gop{Method: http.MethodPut, Path: "/settings/shield"},
	Read:       gop{Path: "/settings/shield"},
	Update:     gop{Method: http.MethodPut, Path: "/settings/shield"},
	DeleteNote: "Destroying the resource leaves the settings as they are; it only stops managing them.",
}

var httpCheckSpec = &gspec{
	Name: "http_check",
	Desc: "A credential expiry check of a project: an `http` check calls an issuer's API with a stored credential, " +
		"a `local` check reads expiring items (certificates, keys, tokens) from a project secret or variable on " +
		"the Temps host and never sends the value anywhere.",
	Perms:   []string{"projects:read", "projects:write"},
	Parents: []string{"project_id"},
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "name", Kind: kString, Required: true, Desc: "Name of the check."},
		{Name: "kind", Kind: kString, Optional: true, OneOf: []string{"http", "local"}, Desc: "Type of check."},
		{Name: "enabled", Kind: kBool, Optional: true, Desc: "Run the check on its interval."},
		{Name: "interval_seconds", Kind: kInt, Optional: true, Desc: "Seconds between runs."},
		{Name: "secret_id", Kind: kInt, Clearable: true, Desc: "ID of the project secret holding the credential or item."},
		{Name: "env_var_id", Kind: kInt, Clearable: true, Desc: "ID of the environment variable holding it."},
		{Name: "credential", Kind: kString, Clearable: true, Sensitive: true, Read: "-", Desc: "Credential sent to the issuer."},
		{Name: "spec", Kind: kJSON, Clearable: true, Read: "-",
			Desc: "HTTP recipe of an `http` check: method, URL, headers and how to read the expiry."},
		{Name: "local", Kind: kJSON, Clearable: true, Read: "-",
			Desc: "Settings of a `local` check, for example `jsonencode({ warning_days = [30, 7] })`."},
		{Name: "automatic_provider", Kind: kString, Computed: true, Desc: "Issuer Temps recognised automatically."},
		{Name: "last_checked_at", Kind: kString, Computed: true, Volatile: true, Desc: "When the check last ran."},
	},
	Create: gop{Method: http.MethodPost, Path: "/projects/{project_id}/http-checks"},
	Read:   gop{Path: "/projects/{project_id}/http-checks"},
	List:   true, ListKey: "items", Paged: true,
	Update: gop{Method: http.MethodPut, Path: "/projects/{project_id}/http-checks/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/projects/{project_id}/http-checks/{id}"},
}
