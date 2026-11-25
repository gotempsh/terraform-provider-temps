// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"net/http"
)

// Status pages, product analytics, error tracking, project policies and
// instance settings.

var statusPageMaintenanceSpec = &gspec{
	Name: "status_page_maintenance",
	Desc: "A scheduled maintenance window shown on a status page; its monitors are not counted as down while it " +
		"runs. Destroying the resource cancels the window.",
	Perms:   statusPagePermissions,
	Parents: []string{"status_page_id"},
	Fields: []gfield{
		{Name: "status_page_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the status page."},
		{Name: "title", Kind: kString, Required: true, Desc: "Title shown to visitors."},
		{Name: "description", Kind: kString, Clearable: true, ClearWith: "", Desc: "What the maintenance is about."},
		{Name: "starts_at", Kind: kString, Required: true, Time: true, Desc: "Start, RFC 3339 (`2030-01-01T02:00:00Z`)."},
		{Name: "ends_at", Kind: kString, Required: true, Time: true, Desc: "End, RFC 3339."},
		{Name: "monitor_ids", Kind: kIntSet, Required: true, Desc: "IDs of the monitors under maintenance."},
		{Name: "state", Kind: kString, Computed: true, Volatile: true, Desc: "`scheduled`, `in_progress` or `completed`."},
	},
	Create: gop{Method: http.MethodPost, Path: "/status-pages/{status_page_id}/maintenances"},
	Read:   gop{Path: "/status-pages/{status_page_id}/maintenances/{id}"},
	Update: gop{Method: http.MethodPatch, Path: "/status-pages/{status_page_id}/maintenances/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/status-pages/{status_page_id}/maintenances/{id}"},
	Gone:   func(obj map[string]any) bool { return obj["state"] == "cancelled" },
}

var statusPageWebhookSpec = &gspec{
	Name: "status_page_webhook",
	Desc: "Notifies a URL when a status page's state changes. Temps signs every delivery with `secret`, shown only " +
		"when the webhook is created (an imported webhook has no `secret`).",
	Perms:   statusPagePermissions,
	Parents: []string{"status_page_id"},
	Fields: []gfield{
		{Name: "status_page_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the status page."},
		{Name: "url", Kind: kString, Required: true, Desc: "HTTPS URL that receives the deliveries."},
		{Name: "enabled", Kind: kBool, Optional: true, Default: true, Desc: "Send deliveries."},
		{Name: "secret", Kind: kString, Computed: true, Sensitive: true, Desc: "Signing secret of the deliveries."},
		{Name: "last_failure_reason", Kind: kString, Computed: true, Volatile: true, Desc: "Why the last delivery failed."},
	},
	Create: gop{Method: http.MethodPost, Path: "/status-pages/{status_page_id}/webhooks", Envelope: "."},
	CreateResult: func(obj map[string]any) map[string]any {
		// {"secret": ..., "webhook": {...}}: the webhook with its secret.
		out := map[string]any{}
		if w, ok := obj["webhook"].(map[string]any); ok {
			for k, v := range w {
				out[k] = v
			}
		}
		out["secret"] = obj["secret"]
		return out
	},
	Read: gop{Path: "/status-pages/{status_page_id}/webhooks"},
	List: true, ListKey: "items", Paged: true,
	Update:   gop{Method: http.MethodPatch, Path: "/status-pages/{status_page_id}/webhooks/{id}"},
	Delete:   gop{Method: http.MethodDelete, Path: "/status-pages/{status_page_id}/webhooks/{id}"},
	BodyHook: dropOnCreate("enabled"),
	AfterWrite: patchAfterCreateIf("enabled", false, http.MethodPatch,
		"/status-pages/{status_page_id}/webhooks/{id}"),
}

var funnelSpec = &gspec{
	Name: "funnel",
	Desc: "A conversion funnel of a project's analytics: the share of visitors who go through `steps` in order. " +
		"Temps does not return a funnel's steps, so steps changed elsewhere are not detected.",
	Perms:   []string{"analytics:read", "analytics:write"},
	Parents: []string{"project_id"},
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "name", Kind: kString, Required: true, Desc: "Name of the funnel."},
		{Name: "description", Kind: kString, Clearable: true, Desc: "What the funnel measures."},
		{Name: "steps", Kind: kJSON, Required: true, Read: "-",
			Desc: "Steps in order: `jsonencode([{ event_name = \"page_view\", event_filter = [] }, { event_name = \"signup\" }])`."},
		{Name: "is_active", Kind: kBool, Computed: true, Volatile: true, Desc: "Whether the funnel is computed."},
	},
	Create:         gop{Method: http.MethodPost, Path: "/projects/{project_id}/funnels"},
	CreateResult:   renameKey("funnel_id", "id"),
	Read:           gop{Path: "/projects/{project_id}/funnels"},
	List:           true,
	Update:         gop{Method: http.MethodPut, Path: "/projects/{project_id}/funnels/{id}"},
	Delete:         gop{Method: http.MethodDelete, Path: "/projects/{project_id}/funnels/{id}"},
	ReadAfterWrite: true,
}

var surveySpec = &gspec{
	Name:    "survey",
	Desc:    "An in-app survey the project's analytics script shows to visitors matching `targeting`.",
	Perms:   []string{"surveys:read", "surveys:write"},
	Parents: []string{"project_id"},
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "name", Kind: kString, Required: true, Desc: "Name of the survey."},
		{Name: "description", Kind: kString, Clearable: true, Desc: "What the survey asks about."},
		{Name: "status", Kind: kString, Optional: true, OneOf: []string{"draft", "active", "paused", "completed"},
			Desc: "Only `active` surveys are shown."},
		{Name: "questions", Kind: kJSON, Optional: true, Subset: true,
			Desc: "Questions: `jsonencode([{ id = \"q1\", kind = \"nps\", prompt = \"How likely are you to recommend us?\" }])`. " +
				"Kinds: `open_text`, `rating`, `nps`, `single_choice`, `multiple_choice`."},
		{Name: "targeting", Kind: kJSON, Optional: true, Subset: true,
			Desc: "Who sees it: `jsonencode({ path_pattern = \"/pricing*\", sample_rate = 0.5, once_per_visitor = true })`."},
	},
	Create: gop{Method: http.MethodPost, Path: "/projects/{project_id}/surveys"},
	Read:   gop{Path: "/projects/{project_id}/surveys/{id}"},
	Update: gop{Method: http.MethodPatch, Path: "/projects/{project_id}/surveys/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/projects/{project_id}/surveys/{id}"},
}

var errorPerms = []string{"error_tracking:read", "error_tracking:write"}

var errorOwnershipRulesSpec = &gspec{
	Name: "error_ownership_rules",
	Desc: "Who owns a project's new errors: the first rule whose pattern matches an error's file or message " +
		"assigns it. Replaces all of the project's rules; destroying the resource removes them.",
	Perms:     errorPerms,
	Parents:   []string{"project_id"},
	Singleton: true,
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "rules", Kind: kJSON, Required: true, Read: "data", Subset: true,
			Desc: "Rules in order: `jsonencode([{ pattern = \"src/billing/**\", owner_user_id = 4 }])`."},
	},
	// Rules come back with server bookkeeping; drop it so an imported rule
	// list matches the configured one.
	Normalize: func(obj map[string]any) {
		rules, _ := obj["data"].([]any)
		for _, r := range rules {
			if m, ok := r.(map[string]any); ok {
				for _, k := range []string{"id", "position", "owner_name", "created_at", "updated_at"} {
					delete(m, k)
				}
			}
		}
	},
	Create: gop{Method: http.MethodPut, Path: "/projects/{project_id}/error-ownership-rules"},
	Read:   gop{Path: "/projects/{project_id}/error-ownership-rules"},
	Update: gop{Method: http.MethodPut, Path: "/projects/{project_id}/error-ownership-rules"},
	Delete: gop{Method: http.MethodPut, Path: "/projects/{project_id}/error-ownership-rules", Body: map[string]any{"rules": []any{}}},
}

var errorSavedSearchSpec = &gspec{
	Name: "error_saved_search",
	Desc: "A saved search over a project's error groups, owned by the API key's user and optionally shared " +
		"with the project's members.",
	Perms:   errorPerms,
	Parents: []string{"project_id"},
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "name", Kind: kString, Required: true, Desc: "Name of the search."},
		{Name: "shared", Kind: kBool, Optional: true, Default: false, Desc: "Show it to every member of the project."},
		{Name: "filters", Kind: kJSON, Optional: true, Subset: true,
			Desc: "Filters: `jsonencode({ status = \"unresolved\", environment_id = 3, search = \"timeout\" })`."},
	},
	// Filters come back with every unset filter as null; drop those so an
	// imported search matches the configured filters.
	Normalize: func(obj map[string]any) {
		if f, ok := obj["filters"].(map[string]any); ok {
			for k, v := range f {
				if v == nil {
					delete(f, k)
				}
			}
		}
	},
	Create: gop{Method: http.MethodPost, Path: "/projects/{project_id}/error-saved-searches"},
	Read:   gop{Path: "/projects/{project_id}/error-saved-searches/{id}"},
	Update: gop{Method: http.MethodPatch, Path: "/projects/{project_id}/error-saved-searches/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/projects/{project_id}/error-saved-searches/{id}"},
}

var errorAttachmentQuotaSpec = &gspec{
	Name:       "error_attachment_quota",
	Desc:       "How many bytes of error attachments (screenshots, minidumps) a project may store.",
	Perms:      errorPerms,
	Parents:    []string{"project_id"},
	Singleton:  true,
	DeleteNote: "Destroying the resource leaves the quota as it is.",
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "quota_bytes", Kind: kInt, Required: true, Desc: "Bytes of attachments the project may store."},
		{Name: "used_bytes", Kind: kInt, Computed: true, Volatile: true, Desc: "Bytes stored now."},
		{Name: "configured", Kind: kBool, Computed: true, Volatile: true, Desc: "Whether attachment storage is set up."},
	},
	Create: gop{Method: http.MethodPut, Path: "/projects/{project_id}/error-attachments/quota"},
	Read:   gop{Path: "/projects/{project_id}/error-attachments/usage"},
	Update: gop{Method: http.MethodPut, Path: "/projects/{project_id}/error-attachments/quota"},
}

// projectScoped turns an instance-wide definition into one of a project.
func projectScoped(base *gspec, name, desc, collection string) *gspec {
	fields := append([]gfield{{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."}},
		base.Fields...)
	p := "/projects/{project_id}/" + collection
	return &gspec{
		Name: name, Desc: desc, Perms: []string{"projects:read", "projects:write"},
		Parents: []string{"project_id"}, IDKey: base.IDKey, Fields: fields,
		Create: gop{Method: http.MethodPost, Path: p},
		Read:   gop{Path: p + "/{id}"},
		Update: gop{Method: http.MethodPut, Path: p + "/{id}"},
		Delete: gop{Method: http.MethodDelete, Path: p + "/{id}"},
	}
}

var projectMcpServerSpec = projectScoped(mcpServerSpec, "project_mcp_server",
	"An MCP server definition only the AI agents of one project can connect to.", "mcp-servers")

var projectSkillSpec = projectScoped(skillSpec, "project_skill",
	"A skill (instructions in Markdown) only the AI agents of one project can load.", "skills")

var projectAutofixSettingsSpec = &gspec{
	Name:       "project_autofix_settings",
	Desc:       "How the autofixer handles a project's new errors: whether it opens fixes, with which model, and which paths it must not touch.",
	Perms:      errorPerms,
	Parents:    []string{"project_id"},
	Singleton:  true,
	DeleteNote: "Destroying the resource leaves the settings as they are.",
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "mode", Kind: kString, Optional: true, Desc: "What the autofixer does, for example `off`, `suggest` or `pull_request`."},
		{Name: "min_confidence", Kind: kInt, Optional: true, Desc: "Lowest evidence score (`0`-`5`) a fix needs before autofix acts on it."},
		{Name: "ai_provider", Kind: kString, Clearable: true, ClearWith: "", Desc: "Model provider; the instance default when unset."},
		{Name: "ai_model", Kind: kString, Clearable: true, ClearWith: "", Desc: "Model; the provider default when unset."},
		{Name: "test_command", Kind: kString, Clearable: true, ClearWith: "", Desc: "Command that must pass before a fix is proposed."},
		{Name: "protected_paths", Kind: kStringList, Optional: true, Desc: "Paths a fix must never change."},
		{Name: "paused", Kind: kBool, Computed: true, Volatile: true, Desc: "Whether the autofixer is paused."},
	},
	Create: gop{Method: http.MethodPatch, Path: "/projects/{project_id}/autofix/settings", Envelope: "."},
	Read:   gop{Path: "/projects/{project_id}/autofix", Envelope: "settings"},
	Update: gop{Method: http.MethodPatch, Path: "/projects/{project_id}/autofix/settings", Envelope: "."},
}

var projectContainerSecuritySpec = &gspec{
	Name: "project_container_security",
	Desc: "Linux capabilities a project's containers may keep beyond Temps' hardened default. Adding one needs " +
		"`acknowledge_risks = true`. Destroying the resource leaves the setting as it is.",
	Perms:     []string{"projects:read", "projects:write"},
	Parents:   []string{"project_id"},
	Singleton: true,
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "allowed_capabilities", Kind: kStringSet, Required: true, Desc: "Capabilities kept, for example `NET_BIND_SERVICE`."},
		{Name: "acknowledge_risks", Kind: kBool, Optional: true, Default: false, Read: "-",
			Desc: "Confirm the trade-offs of the capabilities added."},
	},
	Create:          gop{Method: http.MethodPut, Path: "/projects/{project_id}/container-security"},
	Read:            gop{Path: "/projects/{project_id}/container-security"},
	Update:          gop{Method: http.MethodPut, Path: "/projects/{project_id}/container-security"},
	ExpectedCurrent: map[string]string{"expected_allowed_capabilities": "allowed_capabilities"},
}

var projectComposeSecuritySpec = &gspec{
	Name: "project_compose_security",
	Desc: "Compose file checks Temps skips for a project (by default every check runs and a failing file is " +
		"refused). Turning a check off needs `acknowledge_risks = true`. Destroying the resource leaves the policy as it is.",
	Perms:     []string{"projects:read", "projects:write"},
	Parents:   []string{"project_id"},
	Singleton: true,
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "disabled_checks", Kind: kStringSet, Required: true, JSON: "policy.disabled_checks",
			Desc: "Checks to skip, for example `bind_mounts` or `published_ports`."},
		{Name: "acknowledge_risks", Kind: kBool, Optional: true, Default: false, Read: "-",
			Desc: "Confirm the consequences of the checks turned off."},
		{Name: "acknowledge_legacy_migration", Kind: kBool, Optional: true, Default: false, Read: "-",
			Desc: "Confirm replacing a policy saved by an older Temps."},
	},
	Create:          gop{Method: http.MethodPut, Path: "/projects/{project_id}/compose-security"},
	Read:            gop{Path: "/projects/{project_id}/compose-security"},
	Update:          gop{Method: http.MethodPut, Path: "/projects/{project_id}/compose-security"},
	ExpectedCurrent: map[string]string{"expected_policy": "policy"},
}

var projectIdenticalBuildsSpec = &gspec{
	Name: "project_identical_builds",
	Desc: "What Temps does when a build of the same source is already running or recent: build again, wait " +
		"for it, or reuse its image. Destroying the resource leaves the setting as it is.",
	Perms:     []string{"projects:read", "projects:write"},
	Parents:   []string{"project_id"},
	Singleton: true,
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "mode", Kind: kString, Required: true, OneOf: []string{"parallel", "queue", "share"},
			Desc: "`parallel` builds again, `queue` waits for the running build, `share` reuses its image."},
		{Name: "scope", Kind: kString, Optional: true, OneOf: []string{"fingerprint", "source"},
			Desc: "What makes two builds identical: the build inputs, or the source commit."},
		{Name: "reuse_window_seconds", Kind: kInt, Computed: true, Desc: "How recent a build may be to be reused."},
	},
	Create: gop{Method: http.MethodPut, Path: "/projects/{project_id}/identical-builds"},
	Read:   gop{Path: "/projects/{project_id}/identical-builds"},
	Update: gop{Method: http.MethodPut, Path: "/projects/{project_id}/identical-builds"},
}

var projectDSNSpec = &gspec{
	Name: "project_dsn",
	Desc: "A DSN a Sentry SDK reports a project's errors to, optionally tied to one environment. Destroying the " +
		"resource revokes it; changing it creates a new DSN.",
	Perms:   []string{"error_tracking:read", "error_tracking:create", "error_tracking:write"},
	Parents: []string{"project_id"},
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "name", Kind: kString, Optional: true, Desc: "Name of the DSN."},
		{Name: "environment_id", Kind: kInt, Optional: true, Desc: "Environment the events are filed under."},
		{Name: "base_url", Kind: kString, Optional: true, Read: "-", Desc: "Public URL the DSN points at; the instance URL when unset."},
		{Name: "dsn", Kind: kString, Computed: true, Desc: "The DSN to configure in the SDK."},
		{Name: "public_key", Kind: kString, Computed: true, Desc: "Public key part of the DSN."},
	},
	Create: gop{Method: http.MethodPost, Path: "/projects/{project_id}/dsns"},
	Read:   gop{Path: "/projects/{project_id}/dsns"},
	List:   true,
	Delete: gop{Method: http.MethodPost, Path: "/projects/{project_id}/dsns/{id}/revoke"},
	Gone:   func(obj map[string]any) bool { return obj["is_active"] == false },
}

var environmentWorkflowSettingsSpec = &gspec{
	Name: "environment_workflow_settings",
	Desc: "Limits of the durable workflows an environment runs. Values left out keep their current setting. " +
		"Destroying the resource leaves the settings as they are.",
	Perms:        []string{"workflows:read", "workflows:write"},
	Parents:      []string{"project_id", "environment_id"},
	Singleton:    true,
	MergeCurrent: true,
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "environment_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the environment."},
		{Name: "max_concurrent_runs", Kind: kInt, Optional: true, Desc: "Runs executing at once."},
		{Name: "max_active_runs", Kind: kInt, Optional: true, Desc: "Runs started and not finished."},
		{Name: "max_steps_per_run", Kind: kInt, Optional: true, Desc: "Steps one run may take."},
		{Name: "max_payload_bytes", Kind: kInt, Optional: true, Desc: "Largest input or output of a step."},
		{Name: "inline_payload_limit_bytes", Kind: kInt, Optional: true, Desc: "Largest payload kept in the journal itself."},
		{Name: "max_journal_bytes_per_run", Kind: kInt, Optional: true, Desc: "Journal size of one run."},
		{Name: "invocation_deadline_secs", Kind: kInt, Optional: true, Desc: "Longest a step may run."},
		{Name: "lease_secs", Kind: kInt, Optional: true, Desc: "How long a worker holds a run without reporting."},
		{Name: "drain_timeout_secs", Kind: kInt, Optional: true, Desc: "Grace period for runs when a deployment stops."},
		{Name: "retention_days", Kind: kInt, Optional: true, Desc: "Days finished runs are kept."},
		{Name: "stuck_after_failures", Kind: kInt, Optional: true, Desc: "Failed attempts before a run is marked stuck."},
		{Name: "host_service", Kind: kString, Clearable: true, Desc: "Service of the deployment that hosts the workflows."},
		{Name: "managed_store_service_id", Kind: kInt, Clearable: true, Desc: "Managed database that stores the journals."},
	},
	Create: gop{Method: http.MethodPut, Path: "/projects/{project_id}/environments/{environment_id}/workflows/settings"},
	Read:   gop{Path: "/projects/{project_id}/environments/{environment_id}/workflows/settings"},
	Update: gop{Method: http.MethodPut, Path: "/projects/{project_id}/environments/{environment_id}/workflows/settings"},
}

var projectGroupRouteSpec = &gspec{
	Name: "project_group_route",
	Desc: "Serves a path of one project of a group from another project of the group, in each environment both " +
		"have (`/api` of the web app answered by the API project).",
	Perms:   []string{"projects:read", "projects:write"},
	Parents: []string{"group_id"},
	Fields: []gfield{
		{Name: "group_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project group."},
		{Name: "source_project_id", Kind: kInt, Required: true, Desc: "Project whose address receives the requests."},
		{Name: "path_prefix", Kind: kString, Required: true, Desc: "Path prefix that is routed, for example `/api`."},
		{Name: "target_project_id", Kind: kInt, Required: true, Desc: "Project that answers them."},
		{Name: "strip_prefix", Kind: kBool, Optional: true, Default: false, Desc: "Remove the prefix before forwarding."},
	},
	Create: gop{Method: http.MethodPost, Path: "/project-groups/{group_id}/routes"},
	Read:   gop{Path: "/project-groups/{group_id}/routes"},
	List:   true,
	Delete: gop{Method: http.MethodDelete, Path: "/project-groups/{group_id}/routes/{id}"},
}

var cloudflareAccessProviderSpec = &gspec{
	Name: "cloudflare_access_provider",
	Desc: "A Cloudflare Access account whose signed-in users Temps trusts in front of apps. The token is checked " +
		"before it is saved and never returned.",
	Perms: []string{"dns_providers:read", "dns_providers:write"},
	Fields: []gfield{
		{Name: "name", Kind: kString, Required: true, Desc: "Name of the provider."},
		{Name: "account_id", Kind: kString, Required: true, Desc: "Cloudflare account ID."},
		{Name: "token", Kind: kString, Required: true, Sensitive: true, Read: "-", Desc: "Cloudflare API token that can read Access."},
		{Name: "team_domain", Kind: kString, Computed: true, Desc: "Access team domain."},
	},
	Create: gop{Method: http.MethodPost, Path: "/cloudflare-access/providers"},
	Read:   gop{Path: "/cloudflare-access/providers"},
	List:   true,
	Delete: gop{Method: http.MethodDelete, Path: "/cloudflare-access/providers/{id}"},
}

var nodePublicIngressSpec = &gspec{
	Name: "node_public_ingress",
	Desc: "Whether a worker node accepts visitors' HTTP and HTTPS traffic directly, and on which address and ports. " +
		"Destroying the resource leaves the setting as it is.",
	Perms:     []string{"settings:read", "settings:write"},
	Parents:   []string{"node_id"},
	Singleton: true,
	Fields: []gfield{
		{Name: "node_id", Kind: kInt, Required: true, Path: true, Desc: "ID of a worker node. The control plane (`0`) has no direct worker ingress: its own listeners are set when Temps starts."},
		{Name: "enabled", Kind: kBool, Required: true, Desc: "Accept public traffic on the node."},
		{Name: "http_port", Kind: kInt, Optional: true, Desc: "HTTP port."},
		{Name: "https_port", Kind: kInt, Optional: true, Desc: "HTTPS port."},
		{Name: "listen_address", Kind: kString, Clearable: true, Desc: "Local address to listen on; every address when unset."},
		{Name: "public_address", Kind: kString, Clearable: true, Desc: "Address visitors reach; detected when unset."},
		{Name: "effective_public_address", Kind: kString, Computed: true, Volatile: true, Desc: "Address in use."},
		{Name: "running", Kind: kBool, Computed: true, Volatile: true, Desc: "Whether the listener runs."},
	},
	Create: gop{Method: http.MethodPut, Path: "/internal/nodes/{node_id}/public-ingress/settings"},
	Read:   gop{Path: "/internal/nodes/{node_id}/public-ingress/settings"},
	Update: gop{Method: http.MethodPut, Path: "/internal/nodes/{node_id}/public-ingress/settings"},
}

var ingressHostnameSpec = &gspec{
	Name: "ingress_hostname",
	Desc: "How one hostname reaches visitors: chosen automatically, through a given tunnel " +
		"(`temps_ingress_connector`), or not published. Destroying the resource sets it back to `automatic`.",
	Perms:     []string{"settings:read", "settings:write"},
	Parents:   []string{"hostname"},
	Singleton: true,
	Fields: []gfield{
		{Name: "hostname", Kind: kString, Required: true, Path: true, Desc: "The hostname."},
		{Name: "mode", Kind: kString, Required: true, OneOf: []string{"automatic", "tunnel", "off"}, Desc: "How it is published."},
		{Name: "connector_id", Kind: kInt, Clearable: true, Desc: "Tunnel to publish it through (`tunnel`)."},
		{Name: "state", Kind: kString, Computed: true, Volatile: true, Desc: "Publication state."},
	},
	Create: gop{Method: http.MethodPut, Path: "/ingress/hostnames/{hostname}"},
	Read:   gop{Path: "/ingress/hostnames/{hostname}"},
	Update: gop{Method: http.MethodPut, Path: "/ingress/hostnames/{hostname}"},
	Delete: gop{Method: http.MethodPut, Path: "/ingress/hostnames/{hostname}", Body: map[string]any{"mode": "automatic"}},
}

var accessVaultLinkSpec = &gspec{
	Name:    "access_vault_link",
	Desc:    "Gives a project's sandboxes, or one sandbox, the services of a `temps_access_vault`.",
	Perms:   vaultPerms,
	Parents: []string{"vault_id"},
	Fields: []gfield{
		{Name: "vault_id", Kind: kString, Required: true, Path: true, Desc: "ID of the access vault."},
		{Name: "project_id", Kind: kInt, Optional: true, Desc: "Project whose sandboxes get the vault."},
		{Name: "sandbox_id", Kind: kString, Optional: true, Desc: "One sandbox that gets the vault."},
		{Name: "consumer_name", Kind: kString, Computed: true, Desc: "Name of the project or sandbox."},
	},
	Create: gop{Method: http.MethodPost, Path: "/access-vaults/{vault_id}/links"},
	Read:   gop{Path: "/access-vaults/{vault_id}/links"},
	List:   true, ListKey: "items", Paged: true,
	Delete: gop{Method: http.MethodDelete, Path: "/access-vaults/{vault_id}/links/{id}"},
	Gone:   func(obj map[string]any) bool { return obj["revoked_at"] != nil },
}

var telemetrySettingsSpec = &gspec{
	Name: "telemetry_settings",
	Desc: "Whether the instance sends anonymous usage telemetry. An environment variable on the server " +
		"overrides it (`source`). Destroying the resource leaves the setting as it is.",
	Perms:     []string{"settings:read", "system:admin"},
	Singleton: true,
	Fields: []gfield{
		{Name: "enabled", Kind: kBool, Required: true, Desc: "Send anonymous telemetry."},
		{Name: "source", Kind: kString, Computed: true, Volatile: true, Desc: "Where the effective value comes from."},
	},
	Create: gop{Method: http.MethodPatch, Path: "/settings/telemetry"},
	Read:   gop{Path: "/settings/telemetry"},
	Update: gop{Method: http.MethodPatch, Path: "/settings/telemetry"},
}

var adminGateSettingsSpec = &gspec{
	Name: "admin_gate_settings",
	Desc: "Which hostnames and client addresses may reach the console and API. A list that leaves out the " +
		"address the provider connects from locks it out. Destroying the resource leaves the settings as they are.",
	Perms:        []string{"settings:read", "settings:write"},
	Singleton:    true,
	MergeCurrent: true,
	Fields: []gfield{
		{Name: "allowed_hosts", Kind: kStringList, Optional: true, Desc: "Hostnames the console answers on; any when empty."},
		{Name: "allowed_ips", Kind: kStringList, Optional: true, Desc: "Client addresses or networks allowed; any when empty."},
		{Name: "trust_forwarded_for", Kind: kBool, Optional: true, Desc: "Take the client address from `X-Forwarded-For`."},
		{Name: "source", Kind: kString, Computed: true, Volatile: true, Desc: "Where the effective settings come from."},
	},
	Create: gop{Method: http.MethodPatch, Path: "/admin/gate-settings"},
	Read:   gop{Path: "/admin/gate-settings"},
	Update: gop{Method: http.MethodPatch, Path: "/admin/gate-settings"},
}

var aiModelCatalogSettingsSpec = &gspec{
	Name:       "ai_model_catalog_settings",
	Desc:       "Where the AI gateway's model catalog (models and prices) comes from and how often it is refreshed.",
	Perms:      []string{"ai_gateway:read", "ai_gateway:write"},
	Singleton:  true,
	DeleteNote: "Destroying the resource leaves the settings as they are.",
	Fields: []gfield{
		{Name: "auto_refresh", Kind: kBool, Optional: true, Desc: "Refresh the catalog on a schedule."},
		{Name: "refresh_interval_hours", Kind: kInt, Optional: true, Desc: "Hours between refreshes."},
		{Name: "discover_provider_models", Kind: kBool, Optional: true, Desc: "Also list the models each provider offers."},
		{Name: "source_url", Kind: kString, Clearable: true, Desc: "Catalog URL; the default catalog when unset."},
		{Name: "effective_source_url", Kind: kString, Computed: true, Volatile: true, Desc: "Catalog URL in use."},
	},
	Create: gop{Method: http.MethodPatch, Path: "/ai/model-catalog/settings", Envelope: "."},
	Read:   gop{Path: "/ai/model-catalog", Envelope: "settings"},
	Update: gop{Method: http.MethodPatch, Path: "/ai/model-catalog/settings", Envelope: "."},
}

var aiCodingAgentSettingsSpec = &gspec{
	Name:       "ai_coding_agent_settings",
	Desc:       "Whether Temps keeps the coding-agent CLIs its agents run up to date by itself.",
	Perms:      []string{"ai_gateway:read", "ai_gateway:write"},
	Singleton:  true,
	DeleteNote: "Destroying the resource leaves the setting as it is.",
	Fields: []gfield{
		{Name: "auto_update", Kind: kBool, Required: true, Desc: "Install new releases automatically."},
	},
	Create: gop{Method: http.MethodPatch, Path: "/ai/coding-agents/settings", Envelope: "."},
	Read:   gop{Path: "/ai/coding-agents", Envelope: "settings"},
	Update: gop{Method: http.MethodPatch, Path: "/ai/coding-agents/settings", Envelope: "."},
}

// renameKey moves a create response's identifier to the key reads use.
func renameKey(from, to string) func(map[string]any) map[string]any {
	return func(obj map[string]any) map[string]any {
		if obj == nil {
			return obj
		}
		if v, ok := obj[from]; ok {
			obj[to] = v
		}
		return obj
	}
}
