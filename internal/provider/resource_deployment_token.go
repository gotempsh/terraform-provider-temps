// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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

var deploymentTokenPermissions = []string{"deployment_tokens:read", "deployment_tokens:create", "deployment_tokens:write", "deployment_tokens:delete"}

var (
	_ resource.Resource                = &deploymentTokenResource{}
	_ resource.ResourceWithImportState = &deploymentTokenResource{}
)

type deploymentTokenResource struct{ resourceBase }

func newDeploymentTokenResource() resource.Resource { return &deploymentTokenResource{} }

type deploymentTokenModel struct {
	ID            types.String `tfsdk:"id"`
	ProjectID     types.Int64  `tfsdk:"project_id"`
	EnvironmentID types.Int64  `tfsdk:"environment_id"`
	Name          types.String `tfsdk:"name"`
	Permissions   types.Set    `tfsdk:"permissions"`
	ExpiresAt     types.String `tfsdk:"expires_at"`
	Enabled       types.Bool   `tfsdk:"enabled"`
	Token         types.String `tfsdk:"token"`
	TokenPrefix   types.String `tfsdk:"token_prefix"`
}

// expiredAlready rejects an expiry in the past: the token would never work.
// It runs at apply time because "now" is not stable during planning.
func expiredAlready(expiresAt types.String, diags *diag.Diagnostics) bool {
	if !known(expiresAt) {
		return false
	}
	at, err := time.Parse(time.RFC3339, expiresAt.ValueString())
	if err != nil || at.After(time.Now()) {
		return false
	}
	diags.AddAttributeError(path.Root("expires_at"), "Token would already be expired",
		fmt.Sprintf("expires_at %s is in the past, so the token would never work. Use a future time, or set enabled = false to switch the token off.", expiresAt))
	return true
}

func (r *deploymentTokenResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deployment_token"
}

func (r *deploymentTokenResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A project token that apps and CI use to call Temps (for example to send email or read analytics).\n\n" +
			"Temps returns the token value only when it is created, so it is kept in state as a sensitive value: " +
			"protect the state file. An imported token has no `token` value; replace it to mint a new one.\n\n" +
			"Import with `<project_id>/<token_id>`.\n\nRequired API key permissions: " + permList(deploymentTokenPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric token ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the project.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"environment_id": schema.Int64Attribute{Optional: true,
				MarkdownDescription: "Restrict the token to one environment of `project_id`. Omit it for every environment.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Name, unique within the project.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 255)}},
			"permissions": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType,
				MarkdownDescription: "What the token may do, for example `emails:send` or `analytics:read`; `*` grants full access. " +
					"Omit it for full access.",
				Validators:    []validator.Set{setvalidator.SizeAtLeast(1)},
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()}},
			"expires_at": schema.StringAttribute{Optional: true,
				MarkdownDescription: "RFC 3339 time after which the token stops working, for example `2030-01-01T00:00:00Z`. Must be in the future when created. " +
					"The API cannot remove an expiry, so removing it replaces the token.",
				Validators:    []validator.String{rfc3339{}},
				PlanModifiers: []planmodifier.String{stringRequiresReplaceWhenRemoved("Temps cannot remove a token expiry in place.")}},
			"enabled": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "`false` deactivates the token without deleting it. Defaults to `true`."},
			"token": schema.StringAttribute{Computed: true, Sensitive: true,
				MarkdownDescription: "The token value, known only to the run that created it.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"token_prefix": schema.StringAttribute{Computed: true, MarkdownDescription: "First characters of the token, to recognise it in logs.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		},
	}
}

func (m *deploymentTokenModel) fromAPI(t *client.DeploymentToken) {
	m.ID = types.StringValue(formatID(t.ID))
	m.ProjectID = types.Int64Value(t.ProjectID)
	m.EnvironmentID = types.Int64PointerValue(t.EnvironmentID)
	m.Name = types.StringValue(t.Name)
	if t.Permissions != nil {
		perms := slices.Clone(*t.Permissions)
		slices.Sort(perms)
		m.Permissions = stringSetValue(slices.Compact(perms))
	}
	m.ExpiresAt = timeValue(m.ExpiresAt, t.ExpiresAt)
	if t.IsActive != nil {
		m.Enabled = types.BoolValue(*t.IsActive)
	}
	m.TokenPrefix = types.StringValue(t.TokenPrefix)
	if t.Token != "" {
		m.Token = types.StringValue(t.Token)
	}
}

func (r *deploymentTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan deploymentTokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid := plan.ProjectID.ValueInt64()
	if expiredAlready(plan.ExpiresAt, &resp.Diagnostics) {
		return
	}
	// Temps does not check that the environment belongs to the project, so
	// check here instead of minting a token scoped to someone else's environment.
	if known(plan.EnvironmentID) {
		if _, err := r.client.GetEnvironment(ctx, pid, plan.EnvironmentID.ValueInt64()); err != nil {
			addAPIError(&resp.Diagnostics, fmt.Sprintf("Environment %d is not an environment of project %d", plan.EnvironmentID.ValueInt64(), pid),
				err, deploymentTokenPermissions)
			return
		}
	}
	var perms *[]string
	if known(plan.Permissions) {
		p := stringsFromSet(ctx, plan.Permissions, &resp.Diagnostics)
		perms = &p
	}
	t, err := r.client.CreateDeploymentToken(ctx, pid, client.CreateDeploymentTokenRequest{
		Name: plan.Name.ValueString(), EnvironmentID: int64Ptr(plan.EnvironmentID), ExpiresAt: strPtr(plan.ExpiresAt), Permissions: perms,
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create deployment token "+plan.Name.String()+" in project "+formatID(pid), err, deploymentTokenPermissions)
		return
	}
	plan.fromAPI(t)
	if !plan.Enabled.ValueBool() {
		u, err := r.client.UpdateDeploymentToken(ctx, pid, t.ID, client.UpdateDeploymentTokenRequest{IsActive: ptr(false)})
		if err != nil {
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...) // keep the minted token in state
			addAPIError(&resp.Diagnostics, "Created deployment token "+plan.Name.String()+" but could not deactivate it", err, deploymentTokenPermissions)
			return
		}
		plan.fromAPI(u)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *deploymentTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state deploymentTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_deployment_token ID", err.Error())
		return
	}
	t, err := r.client.GetDeploymentToken(ctx, state.ProjectID.ValueInt64(), id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read deployment token "+formatID(id), err, deploymentTokenPermissions)
		return
	}
	state.fromAPI(t)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *deploymentTokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state deploymentTokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_deployment_token ID", err.Error())
		return
	}
	if !plan.ExpiresAt.Equal(state.ExpiresAt) && expiredAlready(plan.ExpiresAt, &resp.Diagnostics) {
		return
	}
	body := client.UpdateDeploymentTokenRequest{Name: strPtr(plan.Name), IsActive: boolPtr(plan.Enabled), ExpiresAt: strPtr(plan.ExpiresAt)}
	if known(plan.Permissions) {
		p := stringsFromSet(ctx, plan.Permissions, &resp.Diagnostics)
		body.Permissions = &p
	}
	t, err := r.client.UpdateDeploymentToken(ctx, plan.ProjectID.ValueInt64(), id, body)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update deployment token "+formatID(id), err, deploymentTokenPermissions)
		return
	}
	plan.fromAPI(t)
	plan.Token = state.Token
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *deploymentTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state deploymentTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_deployment_token ID", err.Error())
		return
	}
	if err := r.client.DeleteDeploymentToken(ctx, state.ProjectID.ValueInt64(), id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete deployment token "+formatID(id), err, deploymentTokenPermissions)
	}
}

func (r *deploymentTokenResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	pid, id, err := parseCompositeID(req.ID, "<project_id>/<token_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), pid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), formatID(id))...)
}
