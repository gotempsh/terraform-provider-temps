// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var envVarPermissions = []string{"projects:write", "environments:read"}

var (
	_ resource.Resource                   = &envVarResource{}
	_ resource.ResourceWithImportState    = &envVarResource{}
	_ resource.ResourceWithValidateConfig = &envVarResource{}
)

type envVarResource struct{ resourceBase }

func newEnvironmentVariableResource() resource.Resource { return &envVarResource{} }

type envVarModel struct {
	ID               types.String `tfsdk:"id"`
	ProjectID        types.Int64  `tfsdk:"project_id"`
	Key              types.String `tfsdk:"key"`
	Value            types.String `tfsdk:"value"`
	ValueWO          types.String `tfsdk:"value_wo"`
	ValueWOVersion   types.Int64  `tfsdk:"value_wo_version"`
	EnvironmentIDs   types.Set    `tfsdk:"environment_ids"`
	IsSecret         types.Bool   `tfsdk:"is_secret"`
	IncludeInPreview types.Bool   `tfsdk:"include_in_preview"`
}

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (r *envVarResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_environment_variable"
}

func (r *envVarResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An environment variable of a project, applied to one or more of its environments.\n\n" +
			"Secret values (`is_secret = true`) are write-only on the server: the provider keeps the configured value in " +
			"state and never reports drift on it. Plain values are read back, so changes made outside Terraform show up in the plan. " +
			"On Terraform 1.11 and later you can use `value_wo` with `value_wo_version` instead of `value` to keep the value out of state entirely.\n\n" +
			"Turning `is_secret` on updates the variable in place; turning it off replaces it, because a secret value can never be read back.\n\n" +
			"Import with `<project_id>/<variable_id>`; secret values cannot be imported and are set again on the next apply.\n\n" +
			"Required API key permissions: " + permList(envVarPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric variable ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project_id": schema.Int64Attribute{
				Required: true, MarkdownDescription: "ID of the project. Changing it replaces the variable.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"key": schema.StringAttribute{
				Required: true, MarkdownDescription: "Variable name, for example `DATABASE_URL`.",
				Validators: []validator.String{stringvalidator.RegexMatches(envKeyPattern, "must start with a letter or underscore and contain only letters, digits and underscores")},
			},
			"value": schema.StringAttribute{
				Optional: true, Sensitive: true,
				MarkdownDescription: "Variable value. Exactly one of `value` and `value_wo` must be set.",
			},
			"value_wo": schema.StringAttribute{
				Optional: true, Sensitive: true, WriteOnly: true,
				MarkdownDescription: "Write-only variable value (Terraform 1.11+), never stored in state. Bump `value_wo_version` to send a new value.",
			},
			"value_wo_version": schema.Int64Attribute{
				Optional: true, MarkdownDescription: "Change this number to send a new `value_wo`.",
				Validators: []validator.Int64{int64validator.AlsoRequires(path.MatchRoot("value_wo"))},
			},
			"environment_ids": schema.SetAttribute{
				Required: true, ElementType: types.Int64Type,
				MarkdownDescription: "IDs of the environments the variable applies to.",
				Validators:          []validator.Set{setvalidator.SizeAtLeast(1)},
			},
			"is_secret": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Store the value as a write-only secret.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplaceIf(
					func(_ context.Context, req planmodifier.BoolRequest, resp *boolplanmodifier.RequiresReplaceIfFuncResponse) {
						resp.RequiresReplace = req.StateValue.ValueBool() && !req.PlanValue.ValueBool()
					},
					"Turning is_secret off replaces the variable.", "Turning `is_secret` off replaces the variable.",
				)},
			},
			"include_in_preview": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Also inject the variable into preview environments.",
			},
		},
	}
}

func (r *envVarResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg envVarModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.Value.IsUnknown() || cfg.ValueWO.IsUnknown() {
		return
	}
	if cfg.Value.IsNull() == cfg.ValueWO.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("value"), "Exactly one of value and value_wo is required",
			"Set value, or value_wo (Terraform 1.11+) to keep the value out of state.")
	}
}

func (m *envVarModel) fromAPI(v *client.EnvironmentVariable) {
	m.ID = types.StringValue(formatID(v.ID))
	m.Key = types.StringValue(v.Key)
	m.IsSecret = types.BoolValue(v.IsSecret)
	m.IncludeInPreview = types.BoolValue(v.IncludeInPreview)
	ids := make([]int64, len(v.Environments))
	for i, e := range v.Environments {
		ids[i] = e.ID
	}
	m.EnvironmentIDs = int64SetValue(ids)
}

func (r *envVarResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg envVarModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid := plan.ProjectID.ValueInt64()
	value := strPtr(plan.Value)
	if value == nil {
		value = strPtr(cfg.ValueWO) // write-only: only present in configuration
	}
	envIDs := int64sFromSet(ctx, plan.EnvironmentIDs, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.CreateEnvironmentVariable(ctx, pid, client.EnvironmentVariableRequest{
		Key: plan.Key.ValueString(), Value: value, EnvironmentIDs: envIDs,
		IsSecret: boolPtr(plan.IsSecret), IncludeInPreview: plan.IncludeInPreview.ValueBool(),
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create variable "+plan.Key.String()+" in Temps project "+formatID(pid), err, envVarPermissions)
		return
	}
	plan.fromAPI(v)
	plan.ValueWO = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *envVarResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state envVarModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid := state.ProjectID.ValueInt64()
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_environment_variable ID", err.Error())
		return
	}
	v, err := r.client.GetEnvironmentVariable(ctx, pid, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read Temps variable "+formatID(id), err, envVarPermissions)
		return
	}
	// Plain values are readable: refresh them so outside changes show as drift.
	// Secret values are write-only, and value_wo is never stored, so a state
	// that tracks no value (value_wo in use) is left alone. A fresh import has
	// no key yet and gets the plain value.
	importing := state.Key.IsNull()
	tracksValue := !state.Value.IsNull() || importing
	state.fromAPI(v)
	if !v.IsSecret && tracksValue {
		val, err := r.client.GetEnvironmentVariableValue(ctx, pid, id, v.Key)
		switch {
		case err == nil:
			state.Value = types.StringValue(val)
		case client.IsForbidden(err):
			// Became secret between the list and the value read: keep state.
		default:
			addAPIError(&resp.Diagnostics, "Unable to read the value of Temps variable "+formatID(id), err, envVarPermissions)
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *envVarResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, cfg envVarModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid := state.ProjectID.ValueInt64()
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_environment_variable ID", err.Error())
		return
	}
	// The configured value is always known here (value from the plan, or the
	// write-only value from configuration), so send it on every update.
	value := strPtr(plan.Value)
	if value == nil {
		value = strPtr(cfg.ValueWO)
	}
	envIDs := int64sFromSet(ctx, plan.EnvironmentIDs, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.UpdateEnvironmentVariable(ctx, pid, id, client.EnvironmentVariableRequest{
		Key: plan.Key.ValueString(), Value: value, EnvironmentIDs: envIDs,
		IsSecret: boolPtr(plan.IsSecret), IncludeInPreview: plan.IncludeInPreview.ValueBool(),
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update Temps variable "+formatID(id), err, envVarPermissions)
		return
	}
	plan.fromAPI(v)
	plan.ValueWO = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *envVarResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state envVarModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_environment_variable ID", err.Error())
		return
	}
	if err := r.client.DeleteEnvironmentVariable(ctx, state.ProjectID.ValueInt64(), id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete Temps variable "+formatID(id), err, envVarPermissions)
	}
}

func (r *envVarResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	pid, id, err := parseCompositeID(req.ID, "<project_id>/<variable_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), pid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), formatID(id))...)
}
