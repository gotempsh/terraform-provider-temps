// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var (
	_ resource.Resource                = &flagEnvResource{}
	_ resource.ResourceWithImportState = &flagEnvResource{}
)

type flagEnvResource struct{ resourceBase }

func newFeatureFlagEnvironmentResource() resource.Resource { return &flagEnvResource{} }

type flagEnvModel struct {
	ID            types.String `tfsdk:"id"`
	ProjectID     types.Int64  `tfsdk:"project_id"`
	Key           types.String `tfsdk:"key"`
	EnvironmentID types.Int64  `tfsdk:"environment_id"`
	Enabled       types.Bool   `tfsdk:"enabled"`
	Value         types.String `tfsdk:"value"`
}

func (r *flagEnvResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_feature_flag_environment"
}

func (r *flagEnvResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A feature flag's value and kill switch in one environment. Destroying it removes the override, " +
			"so the environment serves the flag default again with the switch on.\n\n" +
			"Import with `<project_id>/<key>/<environment_id>`.\n\nRequired API key permissions: " + permList(flagPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "`<project_id>/<key>/<environment_id>`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the project.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"key": schema.StringAttribute{Required: true, MarkdownDescription: "Key of the flag, usually `temps_feature_flag.<name>.key`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"environment_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of an environment of `project_id`.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"enabled": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "Kill switch. `false` serves the flag default in this environment whatever `value` says. Defaults to `true`."},
			"value": schema.StringAttribute{Optional: true,
				MarkdownDescription: "JSON-encoded value for this environment, for example `jsonencode(true)`; it must match the flag's type. " +
					"Omit it to serve the flag default.",
				Validators: []validator.String{jsonString{}}},
		},
	}
}

func (m *flagEnvModel) fromAPI(e client.FeatureFlagEnvironment) {
	m.ID = types.StringValue(fmt.Sprintf("%d/%s/%d", m.ProjectID.ValueInt64(), m.Key.ValueString(), e.EnvironmentID))
	m.EnvironmentID = types.Int64Value(e.EnvironmentID)
	m.Enabled = types.BoolValue(e.Enabled)
	m.Value = jsonValue(m.Value, e.Value)
}

func (r *flagEnvResource) set(ctx context.Context, plan *flagEnvModel, diags *diag.Diagnostics, verb string) bool {
	pid, key, env := plan.ProjectID.ValueInt64(), plan.Key.ValueString(), plan.EnvironmentID.ValueInt64()
	body := client.SetFeatureFlagEnvironmentRequest{Enabled: plan.Enabled.ValueBool()}
	if known(plan.Value) {
		body.Value = json.RawMessage(plan.Value.ValueString())
	}
	e, err := r.client.SetFeatureFlagEnvironment(ctx, pid, key, env, body)
	if err != nil {
		addAPIError(diags, fmt.Sprintf("Unable to %s flag %q in environment %d of project %d", verb, key, env, pid), err, flagPermissions)
		return false
	}
	plan.fromAPI(*e)
	return true
}

func (r *flagEnvResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan flagEnvModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.set(ctx, &plan, &resp.Diagnostics, "set") {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *flagEnvResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state flagEnvModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid, key, env := state.ProjectID.ValueInt64(), state.Key.ValueString(), state.EnvironmentID.ValueInt64()
	f, err := r.client.GetFeatureFlag(ctx, pid, key)
	if client.IsNotFound(err) || (err == nil && f.ArchivedAt != nil) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to read flag %q in project %d", key, pid), err, flagPermissions)
		return
	}
	// No row means the environment inherits: switch on, no override.
	current := client.FeatureFlagEnvironment{EnvironmentID: env, Enabled: true}
	for _, e := range f.Environments {
		if e.EnvironmentID == env {
			current = e
		}
	}
	state.fromAPI(current)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *flagEnvResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan flagEnvModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.set(ctx, &plan, &resp.Diagnostics, "update") {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *flagEnvResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state flagEnvModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid, key, env := state.ProjectID.ValueInt64(), state.Key.ValueString(), state.EnvironmentID.ValueInt64()
	_, err := r.client.SetFeatureFlagEnvironment(ctx, pid, key, env, client.SetFeatureFlagEnvironmentRequest{Enabled: true})
	if err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to clear flag %q in environment %d of project %d", key, env, pid), err, flagPermissions)
	}
}

func (r *flagEnvResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	pid, key, env, err := parseProjectKeyID(req.ID, true)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), pid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("key"), key)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("environment_id"), env)...)
}
