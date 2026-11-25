// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

// AI gateway: upstream providers and models, spending limits, project agents,
// MCP servers and skills.

var aiPerms = []string{"ai_gateway:read", "ai_gateway:write"}

var aiProviderSpec = &gspec{
	Name: "ai_provider",
	Desc: "An upstream model provider of the Temps AI gateway (OpenAI, Anthropic, a self-hosted OpenAI-compatible " +
		"server, ...) with its API key. Enable or add models with `temps_ai_provider_model`.",
	Perms: aiPerms,
	Fields: []gfield{
		{Name: "provider_type", JSON: "provider", Kind: kString, Required: true, CreateOnly: true,
			Desc: "Provider kind, for example `openai`, `anthropic` or `openai_compatible`."},
		{Name: "display_name", Kind: kString, Required: true, Desc: "Name shown in the console."},
		{Name: "api_key", Kind: kString, Optional: true, Sensitive: true, Read: "-", Desc: "Provider API key."},
		{Name: "base_url", Kind: kString, Optional: true, Desc: "API base URL, for self-hosted or proxied providers."},
		{Name: "default_model", Kind: kString, Optional: true, Desc: "Model used when a request names none."},
		{Name: "capabilities", Kind: kJSON, Optional: true, Subset: true,
			Desc: "What a self-hosted provider supports (chat, embeddings, tools, ...)."},
		{Name: "trust_private_address", Kind: kBool, Optional: true, Default: false, Read: "-",
			Desc: "Allow `base_url` to resolve to a private network address."},
		{Name: "is_active", Kind: kBool, Optional: true, Desc: "Route requests to the provider."},
		{Name: "api_key_masked", Kind: kString, Computed: true, Volatile: true, Desc: "The stored key, masked."},
	},
	Create: gop{Method: http.MethodPost, Path: "/ai/providers", Envelope: "."},
	Read:   gop{Path: "/ai/providers/{id}", Envelope: "key"},
	Update: gop{Method: http.MethodPatch, Path: "/ai/providers/{id}", Envelope: "."},
	Delete: gop{Method: http.MethodDelete, Path: "/ai/providers/{id}"},
	BodyHook: func(body map[string]any, _ gvals, create bool) {
		if create {
			delete(body, "is_active")
		}
	},
}

var aiProviderModelSpec = &gspec{
	Name:    "ai_provider_model",
	Desc:    "A model of a `temps_ai_provider` the gateway offers.",
	Perms:   aiPerms,
	Parents: []string{"provider_id"},
	Fields: []gfield{
		{Name: "provider_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the AI provider."},
		{Name: "model_id", Kind: kString, Required: true, CreateOnly: true, Desc: "Model name at the provider, for example `gpt-4o-mini`."},
		{Name: "display_name", Kind: kString, Optional: true, CreateOnly: true, Desc: "Name shown in the console."},
		{Name: "is_enabled", Kind: kBool, Optional: true, Default: true, Desc: "Offer the model through the gateway."},
		{Name: "is_available", Kind: kBool, Computed: true, Volatile: true, Desc: "Whether the provider still lists the model."},
		{Name: "source", Kind: kString, Computed: true, Desc: "`manual` or how Temps discovered the model."},
	},
	Create: gop{Method: http.MethodPost, Path: "/ai/providers/{provider_id}/models"},
	Read:   gop{Path: "/ai/providers/{provider_id}"},
	List:   true, ListKey: "models",
	Update: gop{Method: http.MethodPatch, Path: "/ai/providers/{provider_id}/models/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/ai/providers/{provider_id}/models/{id}"},
	// Models are created enabled; the update sets is_enabled when configured
	// otherwise, and the create request does not take it.
	BodyHook: func(body map[string]any, _ gvals, create bool) {
		if create {
			delete(body, "is_enabled")
		}
	},
	AfterWrite: func(ctx context.Context, c *client.Client, plan, prior gvals, id string) error {
		en, ok := plan["is_enabled"].(types.Bool)
		if prior != nil || !ok || !known(en) || en.ValueBool() {
			return nil
		}
		_, err := c.Raw(ctx, http.MethodPatch, renderPath("/ai/providers/{provider_id}/models/{id}", plan, id), nil,
			map[string]any{"is_enabled": false})
		return err
	},
}

var aiLimitFields = []gfield{
	{Name: "max_requests_per_minute", Kind: kInt, Clearable: true, Desc: "Requests per minute; unset means no limit."},
	{Name: "max_cost_per_month_microcents", Kind: kInt, Clearable: true,
		Desc: "Spend per calendar month in millionths of a cent; unset means no limit."},
}

var aiInstanceLimitsSpec = &gspec{
	Name:           "ai_instance_limits",
	Desc:           "AI gateway limits for the whole instance. Destroying the resource removes them.",
	Perms:          aiPerms,
	Singleton:      true,
	Fields:         aiLimitFields,
	Create:         gop{Method: http.MethodPut, Path: "/ai/limits/instance", Envelope: "."},
	Read:           gop{Path: "/ai/limits", Envelope: "instance"},
	Update:         gop{Method: http.MethodPut, Path: "/ai/limits/instance", Envelope: "."},
	Delete:         gop{Method: http.MethodPut, Path: "/ai/limits/instance", Body: map[string]any{"max_requests_per_minute": nil, "max_cost_per_month_microcents": nil}},
	BodyHook:       sendNulls("max_requests_per_minute", "max_cost_per_month_microcents"),
	ReadAfterWrite: true,
}

var aiProjectLimitsSpec = &gspec{
	Name:      "ai_project_limits",
	Desc:      "AI gateway limits for one project, overriding the instance limits. Destroying the resource removes the override.",
	Perms:     aiPerms,
	Parents:   []string{"project_id"},
	Singleton: true,
	Fields: append([]gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
	}, aiLimitFields...),
	Create:         gop{Method: http.MethodPut, Path: "/ai/limits/projects/{project_id}", Envelope: "."},
	Read:           gop{Path: "/ai/limits/projects/{project_id}", Envelope: "stored"},
	Update:         gop{Method: http.MethodPut, Path: "/ai/limits/projects/{project_id}", Envelope: "."},
	Delete:         gop{Method: http.MethodDelete, Path: "/ai/limits/projects/{project_id}"},
	BodyHook:       sendNulls("max_requests_per_minute", "max_cost_per_month_microcents"),
	ReadAfterWrite: true,
}

// sendNulls makes a full-replace body carry an explicit null for each unset key.
func sendNulls(keys ...string) func(map[string]any, gvals, bool) {
	return func(body map[string]any, _ gvals, _ bool) {
		for _, k := range keys {
			if _, ok := body[k]; !ok {
				body[k] = nil
			}
		}
	}
}

var projectAgentSpec = &gspec{
	Name:    "project_agent",
	Desc:    "An AI agent of a project: what triggers it, the model and tools it uses, and its budget.",
	Perms:   []string{"projects:read", "projects:write"},
	Parents: []string{"project_id"},
	IDKey:   "slug",
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "slug", Kind: kString, Required: true, CreateOnly: true, Desc: "Identifier of the agent, unique in the project."},
		{Name: "name", Kind: kString, Required: true, Desc: "Name of the agent."},
		{Name: "description", Kind: kString, Clearable: true, Desc: "What the agent does."},
		{Name: "prompt", Kind: kString, Clearable: true, Desc: "Instructions the agent runs with."},
		{Name: "enabled", Kind: kBool, Optional: true, Desc: "Let triggers start the agent."},
		{Name: "ai_provider", Kind: kString, Optional: true, Desc: "Model provider, for example `anthropic`."},
		{Name: "ai_model", Kind: kString, Optional: true, Desc: "Model name."},
		{Name: "ai_provider_key_id", Kind: kInt, Clearable: true, Desc: "ID of the `temps_ai_provider` to bill."},
		{Name: "api_key", Kind: kString, Clearable: true, Sensitive: true, Read: "-", Desc: "Provider API key of this agent alone."},
		{Name: "deliverable", Kind: kString, Optional: true, Desc: "What a run produces, for example `pull_request`."},
		{Name: "branch_prefix", Kind: kString, Optional: true, Desc: "Prefix of the branches the agent pushes."},
		{Name: "config_repo_url", Kind: kString, Clearable: true, Desc: "Repository holding the agent's configuration."},
		{Name: "config_repo_branch", Kind: kString, Clearable: true, Desc: "Branch of `config_repo_url`."},
		{Name: "max_turns", Kind: kInt, Optional: true, Desc: "Most model turns per run."},
		{Name: "timeout_seconds", Kind: kInt, Optional: true, Desc: "Longest a run may take."},
		{Name: "cooldown_minutes", Kind: kInt, Optional: true, Desc: "Least time between two runs."},
		{Name: "daily_budget_cents", Kind: kInt, Optional: true, Desc: "Most the agent may spend a day, in cents."},
		{Name: "sandbox_enabled", Kind: kBool, Optional: true, Desc: "Run the agent in a sandbox."},
		{Name: "trigger_config", Kind: kJSON, Optional: true, Subset: true, Desc: "What starts a run (schedule, webhook, events)."},
		{Name: "tools_config", Kind: kJSON, Optional: true, Subset: true, Desc: "Tools the agent may use."},
		{Name: "mcp_servers_config", Kind: kJSON, Optional: true, Subset: true, Desc: "MCP servers the agent connects to."},
		{Name: "skills_config", Kind: kJSON, Optional: true, Subset: true, Desc: "Skills the agent loads."},
		{Name: "webhook_url", Kind: kString, Computed: true, Desc: "URL that starts a run when the agent has a webhook trigger."},
		{Name: "source", Kind: kString, Computed: true, Desc: "Where the agent's definition comes from."},
	},
	Create: gop{Method: http.MethodPost, Path: "/projects/{project_id}/agents"},
	Read:   gop{Path: "/projects/{project_id}/agents/{id}"},
	Update: gop{Method: http.MethodPut, Path: "/projects/{project_id}/agents/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/projects/{project_id}/agents/{id}"},
}

var mcpServerSpec = &gspec{
	Name:  "mcp_server",
	Desc:  "An MCP server definition every AI agent of the instance can connect to.",
	Perms: []string{"settings:read", "settings:write"},
	IDKey: "slug",
	Fields: []gfield{
		{Name: "slug", Kind: kString, Required: true, CreateOnly: true, Desc: "Identifier of the server."},
		{Name: "name", Kind: kString, Required: true, Desc: "Name shown in the console."},
		{Name: "description", Kind: kString, Clearable: true, ClearWith: "", Desc: "What the server provides."},
		{Name: "config", Kind: kJSON, Required: true, Sensitive: true, Subset: true,
			Desc: "Server configuration: `command` and `args` for a local server, or `url` and `headers` for a remote one."},
	},
	Create: gop{Method: http.MethodPost, Path: "/settings/mcp-servers"},
	Read:   gop{Path: "/settings/mcp-servers/{id}"},
	Update: gop{Method: http.MethodPut, Path: "/settings/mcp-servers/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/settings/mcp-servers/{id}"},
}

var skillSpec = &gspec{
	Name:  "skill",
	Desc:  "A skill (instructions in Markdown) every AI agent of the instance can load.",
	Perms: []string{"settings:read", "settings:write"},
	IDKey: "slug",
	Fields: []gfield{
		{Name: "slug", Kind: kString, Required: true, CreateOnly: true, Desc: "Identifier of the skill."},
		{Name: "name", Kind: kString, Required: true, Desc: "Name of the skill."},
		{Name: "description", Kind: kString, Clearable: true, ClearWith: "", Desc: "When an agent should use it."},
		{Name: "content", Kind: kString, Required: true, Desc: "The skill's Markdown."},
		{Name: "has_archive", Kind: kBool, Computed: true, Desc: "Whether supporting files were uploaded with it."},
	},
	Create: gop{Method: http.MethodPost, Path: "/settings/skills"},
	Read:   gop{Path: "/settings/skills/{id}"},
	Update: gop{Method: http.MethodPut, Path: "/settings/skills/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/settings/skills/{id}"},
}
