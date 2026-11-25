// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var apiKeyPermissions = []string{"api_keys:read", "api_keys:create", "api_keys:write", "api_keys:delete"}

var apiKeyRoles = []string{"admin", "platform_admin", "user", "reader", "api_reader", "custom"}

var (
	_ resource.Resource                   = &apiKeyResource{}
	_ resource.ResourceWithImportState    = &apiKeyResource{}
	_ resource.ResourceWithValidateConfig = &apiKeyResource{}
)

type apiKeyResource struct{ resourceBase }

func newAPIKeyResource() resource.Resource { return &apiKeyResource{} }

type apiKeyModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	Role          types.String `tfsdk:"role"`
	Permissions   types.Set    `tfsdk:"permissions"`
	ProjectID     types.Int64  `tfsdk:"project_id"`
	EnvironmentID types.Int64  `tfsdk:"environment_id"`
	ExpiresAt     types.String `tfsdk:"expires_at"`
	Enabled       types.Bool   `tfsdk:"enabled"`
	Key           types.String `tfsdk:"key"`
	KeyPrefix     types.String `tfsdk:"key_prefix"`
}

func (r *apiKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_key"
}

func (r *apiKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Temps API key, for CI, scripts or another Terraform workspace.\n\n" +
			"Temps returns the key only when it is created, so it is kept in state as a sensitive value: protect the " +
			"state file. An imported key has no `key` value; replace it to mint a new one.\n\n" +
			"A key can never grant more than the key that creates it: Temps refuses a role or permission the provider's " +
			"own key lacks. Temps cannot change a key's role, permissions or scope, so changing them replaces the key.\n\n" +
			"Import with the key ID.\n\nRequired API key permissions: " + permList(apiKeyPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric key ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Name, unique on the server.",
				Validators: []validator.String{notBlank{}, stringvalidator.LengthAtMost(255)}},
			"role": schema.StringAttribute{Required: true,
				MarkdownDescription: "Role: one of `" + joinTicks(apiKeyRoles) + "`. `custom` takes `permissions`.",
				Validators:          []validator.String{stringvalidator.OneOf(apiKeyRoles...)},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"permissions": schema.SetAttribute{Optional: true, ElementType: types.StringType,
				MarkdownDescription: "For `role = \"custom\"` only: the permissions the key has, for example `projects:read`. " +
					"Changing them replaces the key.",
				Validators:    []validator.Set{setvalidator.SizeAtLeast(1)},
				PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()}},
			"project_id": schema.Int64Attribute{Optional: true,
				MarkdownDescription: "Restrict the key to one project.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"environment_id": schema.Int64Attribute{Optional: true,
				MarkdownDescription: "Restrict the key to one environment of `project_id`.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"expires_at": schema.StringAttribute{Optional: true, Computed: true,
				MarkdownDescription: "RFC 3339 time after which the key stops working. Must be in the future. " +
					"Unset, Temps expires the key one year after it is created.",
				Validators:    []validator.String{rfc3339{}},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"enabled": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "`false` deactivates the key without deleting it. Defaults to `true`."},
			"key": schema.StringAttribute{Computed: true, Sensitive: true,
				MarkdownDescription: "The key, known only to the run that created it.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"key_prefix": schema.StringAttribute{Computed: true, MarkdownDescription: "First characters of the key, to recognise it in logs.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		},
	}
}

func (r *apiKeyResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg apiKeyModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || !known(cfg.Role) || cfg.Permissions.IsUnknown() {
		return
	}
	custom := cfg.Role.ValueString() == "custom"
	switch {
	case custom && cfg.Permissions.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("permissions"), "Missing permissions",
			`role = "custom" needs permissions, for example ["projects:read"].`)
	case !custom && !cfg.Permissions.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("permissions"), "Permissions need the custom role",
			`Temps ignores permissions for role "`+cfg.Role.ValueString()+`"; use role = "custom" to choose them.`)
	}
	if !cfg.EnvironmentID.IsNull() && cfg.ProjectID.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("project_id"), "Missing project_id",
			"environment_id restricts the key to an environment of project_id, so project_id is required too.")
	}
}

func (m *apiKeyModel) fromAPI(k *client.APIKey) {
	m.ID = types.StringValue(formatID(k.ID))
	m.Name = types.StringValue(k.Name)
	m.Role = types.StringValue(k.RoleType)
	if k.Permissions != nil && k.RoleType == "custom" {
		perms := slices.Clone(*k.Permissions)
		slices.Sort(perms)
		m.Permissions = stringSetValue(slices.Compact(perms))
	} else {
		m.Permissions = types.SetNull(types.StringType)
	}
	m.ProjectID = types.Int64PointerValue(k.ProjectID)
	m.EnvironmentID = types.Int64PointerValue(k.EnvironmentID)
	m.ExpiresAt = timeValue(m.ExpiresAt, k.ExpiresAt)
	if k.IsActive != nil {
		m.Enabled = types.BoolValue(*k.IsActive)
	}
	m.KeyPrefix = types.StringValue(k.KeyPrefix)
	if k.APIKey != "" {
		m.Key = types.StringValue(k.APIKey)
	}
}

func (r *apiKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan apiKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || expiredAlready(plan.ExpiresAt, &resp.Diagnostics) {
		return
	}
	body := client.CreateAPIKeyRequest{
		Name: plan.Name.ValueString(), RoleType: plan.Role.ValueString(),
		ProjectID: int64Ptr(plan.ProjectID), EnvironmentID: int64Ptr(plan.EnvironmentID), ExpiresAt: strPtr(plan.ExpiresAt),
	}
	if known(plan.Permissions) {
		perms := stringsFromSet(ctx, plan.Permissions, &resp.Diagnostics)
		body.Permissions = &perms
	}
	k, err := r.client.CreateAPIKey(ctx, body)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create API key "+plan.Name.ValueString(), err, apiKeyPermissions)
		return
	}
	plan.Key = types.StringNull()
	plan.fromAPI(k)
	// Created active; switch it off in a second call when asked to.
	if !plan.Enabled.ValueBool() {
		k, err = r.client.UpdateAPIKey(ctx, k.ID, client.UpdateAPIKeyRequest{IsActive: ptr(false)})
		if err != nil {
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			addAPIError(&resp.Diagnostics, "API key "+formatID(plan.mustID())+" was created but could not be deactivated", err, apiKeyPermissions)
			return
		}
		plan.fromAPI(k)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (m *apiKeyModel) mustID() int64 {
	id, _ := parseID(m.ID.ValueString())
	return id
}

func (r *apiKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state apiKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_api_key ID", err.Error())
		return
	}
	k, err := r.client.GetAPIKey(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read API key "+formatID(id), err, apiKeyPermissions)
		return
	}
	state.fromAPI(k)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *apiKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state apiKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_api_key ID", err.Error())
		return
	}
	body := client.UpdateAPIKeyRequest{IsActive: boolPtr(plan.Enabled)}
	if !plan.Name.Equal(state.Name) {
		body.Name = strPtr(plan.Name)
	}
	if known(plan.ExpiresAt) && !plan.ExpiresAt.Equal(state.ExpiresAt) {
		if expiredAlready(plan.ExpiresAt, &resp.Diagnostics) {
			return
		}
		body.ExpiresAt = strPtr(plan.ExpiresAt)
	}
	k, err := r.client.UpdateAPIKey(ctx, id, body)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update API key "+formatID(id), err, apiKeyPermissions)
		return
	}
	plan.fromAPI(k)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *apiKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state apiKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_api_key ID", err.Error())
		return
	}
	if err := r.client.DeleteAPIKey(ctx, id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete API key "+formatID(id), err, apiKeyPermissions)
	}
}

func (r *apiKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
