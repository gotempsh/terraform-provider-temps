// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

// Users, roles, single sign-on, Git providers and instance-wide settings.

var userPerms = []string{"users:read", "users:create", "users:write", "users:delete"}

// userRoleNames turns the role objects of a user response into their names.
func userRoleNames(obj map[string]any) {
	roles, _ := obj["roles"].([]any)
	names := make([]any, 0, len(roles))
	for _, r := range roles {
		if m, ok := r.(map[string]any); ok {
			names = append(names, m["name"])
		}
	}
	obj["roles"] = names
}

func userDeleted(obj map[string]any) bool {
	d, ok := getPath(obj, "user.deleted_at")
	return ok && d != nil
}

var userSpec = &gspec{
	Name: "user",
	Desc: "A Temps console user. `initial_roles` and `password` only seed the account; assign further roles with " +
		"`temps_user_role` and let the user change their own password. Destroying the resource deletes the user, " +
		"which an administrator can restore from the console.",
	Perms: userPerms,
	IDKey: "user.id",
	Fields: []gfield{
		{Name: "username", Kind: kString, Required: true, Read: "user.username",
			Desc: "User name, also shown as the display name. Changing it renames the user in place."},
		{Name: "email", Kind: kString, Optional: true, Read: "user.email", Desc: "Email address."},
		{Name: "password", Kind: kString, Optional: true, Sensitive: true, Initial: true, Read: "-",
			Desc: "Starting password. Omit it to have Temps generate one."},
		{Name: "must_change_password", Kind: kBool, Optional: true, Initial: true, Read: "-",
			Desc: "Ask the user to choose a new password at first sign-in."},
		{Name: "initial_roles", JSON: "roles", Kind: kStringSet, Required: true, Initial: true,
			Desc: "Roles the user starts with, for example `user` or `admin`."},
		{Name: "roles", Kind: kStringSet, Computed: true, Volatile: true, Desc: "Roles the user holds now."},
		{Name: "mfa_enabled", Kind: kBool, Computed: true, Volatile: true, Read: "user.mfa_enabled",
			Desc: "Whether the user has turned on multi-factor authentication."},
	},
	Create:    gop{Method: http.MethodPost, Path: "/users"},
	Read:      gop{Path: "/users"},
	List:      true,
	Update:    gop{Method: http.MethodPatch, Path: "/users/{id}"},
	Delete:    gop{Method: http.MethodDelete, Path: "/users/{id}"},
	Normalize: userRoleNames,
	Gone:      userDeleted,
	// The user name is one stored value: create takes it as "username",
	// update as "name".
	BodyHook: func(body map[string]any, _ gvals, create bool) {
		if !create {
			if u, ok := body["username"]; ok {
				body["name"] = u
				delete(body, "username")
			}
		}
	},
}

var userRoleSpec = &gspec{
	Name:    "user_role",
	Desc:    "Grants a role to a Temps user. Destroying it removes the role.",
	Perms:   userPerms,
	Parents: []string{"user_id"},
	IDAttr:  "role_type",
	Fields: []gfield{
		{Name: "user_id", Kind: kInt, Required: true, Desc: "ID of the user."},
		{Name: "role_type", Kind: kString, Required: true, Desc: "Role name, for example `admin` or `user`."},
	},
	Create: gop{Method: http.MethodPost, Path: "/users/{user_id}/roles"},
	Delete: gop{Method: http.MethodDelete, Path: "/users/{user_id}/roles/{id}"},
	ReadHook: func(ctx context.Context, c *client.Client, v gvals, id string) (map[string]any, error) {
		raw, err := c.Raw(ctx, http.MethodGet, "/users", nil, nil)
		if err != nil {
			return nil, err
		}
		uid := scalarString(v["user_id"])
		list, _ := raw.([]any)
		for _, it := range list {
			obj := asObject(it)
			if u, _ := getPath(obj, "user.id"); rawString(u) != uid || userDeleted(obj) {
				continue
			}
			userRoleNames(obj)
			for _, r := range obj["roles"].([]any) {
				if rawString(r) == id {
					return map[string]any{"role_type": id}, nil
				}
			}
		}
		return nil, nil
	},
}

var oidcPerms = []string{"settings:read", "settings:write"}

var oidcProviderSpec = &gspec{
	Name: "oidc_provider",
	Desc: "An OpenID Connect identity provider people sign in to the console with. Map its groups to Temps roles " +
		"with `temps_oidc_role_mapping`.",
	Perms: oidcPerms,
	Fields: []gfield{
		{Name: "name", Kind: kString, Required: true, Desc: "Name shown on the sign-in button."},
		{Name: "issuer_url", Kind: kString, Required: true, Desc: "Issuer URL; Temps reads its discovery document."},
		{Name: "client_id", Kind: kString, Required: true, Desc: "OAuth client ID."},
		{Name: "client_secret", Kind: kString, Required: true, Sensitive: true, Read: "-", Desc: "OAuth client secret."},
		{Name: "scopes", Kind: kString, Optional: true, Desc: "Space-separated scopes to request."},
		{Name: "default_role", Kind: kString, Optional: true, Desc: "Role given to users no mapping matches."},
		{Name: "group_claim", Kind: kString, Optional: true, Desc: "ID-token claim holding the user's groups."},
		{Name: "role_claim", Kind: kString, Optional: true, Desc: "ID-token claim holding a role name."},
		{Name: "template", Kind: kString, Optional: true, Desc: "Provider template the console used, for example `generic`."},
		{Name: "enabled", Kind: kBool, Optional: true, Desc: "Offer the provider on the sign-in page."},
		{Name: "jit_provisioning", Kind: kBool, Optional: true, Desc: "Create users on their first sign-in."},
		{Name: "trust_idp_email", Kind: kBool, Optional: true,
			Desc: "Trust the email address the provider sends even if it does not mark it verified."},
		{Name: "managed_by_cloud", Kind: kBool, Computed: true, Desc: "Whether Temps Cloud manages the provider."},
	},
	Create: gop{Method: http.MethodPost, Path: "/admin/oidc/providers"},
	Read:   gop{Path: "/admin/oidc/providers"},
	List:   true,
	Update: gop{Method: http.MethodPatch, Path: "/admin/oidc/providers/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/admin/oidc/providers/{id}"},
}

var oidcRoleMappingSpec = &gspec{
	Name:    "oidc_role_mapping",
	Desc:    "Gives users in an identity-provider group a Temps role when they sign in through `temps_oidc_provider`.",
	Perms:   oidcPerms,
	Parents: []string{"provider_id"},
	Fields: []gfield{
		{Name: "provider_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the OIDC provider."},
		{Name: "idp_group", Kind: kString, Required: true, Desc: "Group name as the provider sends it."},
		{Name: "role", Kind: kString, Required: true, Desc: "Temps role to grant, for example `admin`."},
		{Name: "priority", Kind: kInt, Required: true, Desc: "Higher priorities win when a user is in several mapped groups."},
	},
	Create: gop{Method: http.MethodPost, Path: "/admin/oidc/providers/{provider_id}/role-mappings"},
	Read:   gop{Path: "/admin/oidc/providers/{provider_id}/role-mappings"},
	List:   true,
	Delete: gop{Method: http.MethodDelete, Path: "/admin/oidc/role-mappings/{id}"},
}

var gitProviderCreatePaths = map[string]string{
	"github":    "/git-providers/github/pat",
	"gitlab":    "/git-providers/gitlab/pat",
	"gitea":     "/git-providers/gitea/pat",
	"bitbucket": "/git-providers/bitbucket",
	"generic":   "/git-providers/generic",
}

var gitProviderSpec = &gspec{
	Name: "git_provider",
	Desc: "A Git host Temps clones repositories from, connected with an access token. GitHub and GitLab apps " +
		"that need a browser authorization flow are set up in the console instead.\n\n" +
		"`kind` picks the host: `github`, `gitlab` and `gitea` take `token`; `bitbucket` takes `token` as an " +
		"access token, or with `token_username` as an app password; `generic` takes `clone_url` and an optional " +
		"`token` and `token_username`. Changing `token` updates the stored credential in place.",
	Perms: []string{"git_providers:read", "git_providers:create", "git_providers:write", "git_providers:delete"},
	Fields: []gfield{
		{Name: "kind", Kind: kString, Required: true, Path: true, OneOf: sortedKeys(gitProviderCreatePaths),
			Read: "provider_type", Desc: "Type of Git host."},
		{Name: "name", Kind: kString, Required: true, CreateOnly: true, Desc: "Name of the connection."},
		{Name: "base_url", Kind: kString, Optional: true, CreateOnly: true,
			Desc: "URL of a self-hosted GitLab or Gitea, or of a generic host's web interface."},
		{Name: "clone_url", Kind: kString, Optional: true, CreateOnly: true, Read: "-",
			Desc: "Clone URL prefix of a `generic` host."},
		{Name: "token", Kind: kString, Optional: true, Sensitive: true, Read: "-",
			Desc: "Access token, or Bitbucket app password when `token_username` is set. Temps can only update a " +
				"personal access token in place: changing the token of a `generic` host or a Bitbucket app " +
				"password replaces the connection.",
			ReplaceIf: gitTokenNotEditable, ReplaceIfDesc: "Temps cannot edit basic-auth credentials in place."},
		{Name: "token_username", Kind: kString, Optional: true, CreateOnly: true, Read: "-",
			Desc: "User name sent with the token (Bitbucket app passwords and generic hosts)."},
		{Name: "auth_method", Kind: kString, Computed: true, Desc: "How Temps authenticates to the host."},
		{Name: "is_active", Kind: kBool, Computed: true, Volatile: true, Desc: "Whether the connection is active."},
		{Name: "is_default", Kind: kBool, Computed: true, Volatile: true, Desc: "Whether it is the default provider."},
	},
	Create: gop{Method: http.MethodPost, PathFn: func(v gvals, _ string) string {
		return gitProviderCreatePaths[scalarString(v["kind"])]
	}},
	Read:   gop{Path: "/git-providers/{id}"},
	Update: gop{Method: http.MethodPatch, Path: "/git-providers/{id}/credentials"},
	Delete: gop{Method: http.MethodDelete, Path: "/git-providers/{id}"},
	BodyHook: func(body map[string]any, v gvals, create bool) {
		kind := scalarString(v["kind"])
		if !create {
			// Only a personal access token can be edited; a changed basic-auth
			// token replaces the resource instead (gitTokenNotEditable).
			if _, user := v["token_username"].(types.String); kind == "generic" || (kind == "bitbucket" && user && known(v["token_username"])) {
				delete(body, "token")
			}
			return
		}
		if kind != "bitbucket" {
			return
		}
		auth := map[string]any{"type": "access_token", "token": body["token"]}
		if u, ok := body["token_username"]; ok {
			auth = map[string]any{"type": "app_password", "username": u, "password": body["token"]}
		}
		delete(body, "token")
		delete(body, "token_username")
		body["auth"] = auth
	},
}

// gitTokenNotEditable: Temps edits only personal-access-token credentials;
// generic hosts and Bitbucket app passwords use basic auth.
func gitTokenNotEditable(ctx context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
	if req.StateValue.IsNull() {
		return // imported: Temps never returns the token
	}
	var kind, user types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("kind"), &kind)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("token_username"), &user)...)
	switch kind.ValueString() {
	case "generic":
		resp.RequiresReplace = true
	case "bitbucket":
		resp.RequiresReplace = known(user)
	}
}

var settingsSpec = &gspec{
	Name: "settings",
	Desc: "Instance-wide Temps settings, as one JSON document in `values` that is merged into the stored settings: " +
		"keys you leave out keep their current value, and refresh only compares the keys you set. Secrets Temps " +
		"masks on read (registry passwords, DNS tokens) cannot be compared and are taken as unchanged.",
	Perms:     []string{"settings:read", "settings:write"},
	Singleton: true,
	Fields: []gfield{
		{Name: "values", JSON: ".", Kind: kJSON, Required: true, Subset: true,
			Desc: "Settings to apply, using the field names of `GET /api/settings`, for example " +
				"`jsonencode({ preview_domain = \"preview.example.com\", security_headers = { enabled = true } })`."},
	},
	Create:         gop{Method: http.MethodPut, Path: "/settings"},
	Read:           gop{Path: "/settings"},
	Update:         gop{Method: http.MethodPut, Path: "/settings"},
	ReadAfterWrite: true,
	DeleteNote:     "Destroying the resource leaves the settings as they are; it only stops managing them.",
}

var globalSecretSpec = &gspec{
	Name: "global_secret",
	Desc: "A secret stored in Temps' instance secret store, for example a registry password a build reads. " +
		"Changing `value` overwrites it in place.",
	Perms: []string{"secrets:read", "secrets:write", "secrets:delete"},
	IDKey: "name",
	Fields: []gfield{
		{Name: "name", Kind: kString, Required: true, ForceNew: true, Desc: "Unique name of the secret."},
		{Name: "value", Kind: kString, Required: true, Sensitive: true, Read: "-", Desc: "Secret value."},
		{Name: "description", Kind: kString, Clearable: true, Desc: "What the secret is for."},
		{Name: "secret_type", Kind: kString, Optional: true, Desc: "Type of secret, for example `env` or `file`."},
		{Name: "mount_path", Kind: kString, Clearable: true, Desc: "Where a `file` secret is mounted."},
		{Name: "project_id", Kind: kInt, Optional: true, ForceNew: true, Desc: "Limit the secret to one project."},
	},
	Create:  gop{Method: http.MethodPost, Path: "/settings/secrets"},
	Read:    gop{Path: "/settings/secrets"},
	List:    true,
	ListKey: "items",
	Update:  gop{Method: http.MethodPost, Path: "/settings/secrets"},
	Delete:  gop{Method: http.MethodDelete, Path: "/settings/secrets/{id}"},
}

var globalIPRuleSpec = &gspec{
	Name: "global_ip_access_rule",
	Desc: "Allows or blocks a client address or network for every app the Temps proxy serves and for the console. " +
		"Temps refuses a block rule matching the address Terraform connects from unless `acknowledge_lockout` is " +
		"`true`, and one broader than /8 (IPv4) or /16 (IPv6) unless `confirm_broad_range` is `true`.",
	Perms: []string{"ip_access_control:read", "ip_access_control:write"},
	Fields: []gfield{
		{Name: "ip_address", Kind: kString, Required: true, Desc: "Address or CIDR network, for example `203.0.113.0/24`."},
		{Name: "action", Kind: kString, Required: true, OneOf: []string{"allow", "block"}, Desc: "What to do with matching clients."},
		{Name: "reason", Kind: kString, Clearable: true, Desc: "Why the rule exists."},
		{Name: "acknowledge_lockout", Kind: kBool, Optional: true, Default: false, Read: "-",
			Desc: "Save a block rule even if it matches the address Terraform connects from."},
		{Name: "confirm_broad_range", Kind: kBool, Optional: true, Default: false, Read: "-",
			Desc: "Save a block rule broader than /8 (IPv4) or /16 (IPv6)."},
	},
	Create: gop{Method: http.MethodPost, Path: "/ip-access-control"},
	Read:   gop{Path: "/ip-access-control/{id}"},
	Update: gop{Method: http.MethodPatch, Path: "/ip-access-control/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/ip-access-control/{id}"},
}
