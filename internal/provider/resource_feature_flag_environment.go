// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

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
	ID            types.String    `tfsdk:"id"`
	ProjectID     types.Int64     `tfsdk:"project_id"`
	Key           types.String    `tfsdk:"key"`
	EnvironmentID types.Int64     `tfsdk:"environment_id"`
	Enabled       types.Bool      `tfsdk:"enabled"`
	Value         types.String    `tfsdk:"value"`
	Rules         []flagRuleModel `tfsdk:"rules"`
}

func (r *flagEnvResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_feature_flag_environment"
}

func (r *flagEnvResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A feature flag's value, kill switch and targeting rules in one environment. Destroying it removes the override " +
			"(and the rules it manages), so the environment serves the flag default again with the switch on.\n\n" +
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
			"rules": flagRulesAttribute(),
		},
	}
}

// fromAPI copies the environment's state. Rules are read only while
// managed (non-null in m) or, with rules set, after an import; a server
// without rules leaves them as they are.
func (m *flagEnvModel) fromAPI(e client.FeatureFlagEnvironment, imported bool, diags *diag.Diagnostics) {
	m.ID = types.StringValue(fmt.Sprintf("%d/%s/%d", m.ProjectID.ValueInt64(), m.Key.ValueString(), e.EnvironmentID))
	m.EnvironmentID = types.Int64Value(e.EnvironmentID)
	m.Enabled = types.BoolValue(e.Enabled)
	m.Value = jsonValue(m.Value, e.Value)
	if e.Rules == nil || (m.Rules == nil && (!imported || len(*e.Rules) == 0)) {
		return
	}
	if e.RulesValid != nil && !*e.RulesValid {
		diags.AddWarning("Stored targeting rules are invalid",
			fmt.Sprintf("Temps could not decode the targeting rules of flag %q in environment %d; it serves the flag default until they are replaced.",
				m.Key.ValueString(), e.EnvironmentID))
	}
	rules, err := flagRulesFromAPI(*e.Rules, m.Rules)
	if err != nil {
		diags.AddError("Unexpected targeting rules from Temps", err.Error())
		return
	}
	m.Rules = rules
}

// set writes the value and kill switch, then the rules when they are
// configured and differ from prior (nil on create). It reports whether plan
// now holds state to save: true once the value write succeeded, even if the
// rules write then failed (plan keeps the prior rules, so the next apply
// retries them and the override is never left untracked).
func (r *flagEnvResource) set(ctx context.Context, plan, prior *flagEnvModel, diags *diag.Diagnostics, verb string) bool {
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
	configured := plan.Rules
	if configured != nil && (prior == nil || !reflect.DeepEqual(configured, prior.Rules)) {
		// The value write just saved the row: its revision guards the rules.
		withRules, err := r.client.SetFeatureFlagRules(ctx, pid, key, env, flagRulesRequest(configured), e.UpdatedAt)
		if err != nil {
			addAPIError(diags, fmt.Sprintf("Unable to set the targeting rules of flag %q in environment %d of project %d", key, env, pid), err, flagPermissions)
			plan.Rules = nil
			if prior != nil {
				plan.Rules = prior.Rules
			}
			plan.fromAPI(*e, false, diags)
			return true
		}
		e = withRules
	}
	plan.fromAPI(*e, false, diags)
	if configured == nil {
		plan.Rules = nil
	}
	return true
}

func (r *flagEnvResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan flagEnvModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.set(ctx, &plan, nil, &resp.Diagnostics, "set") {
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
	// No row means the environment inherits: switch on, no override, no rules.
	current := client.FeatureFlagEnvironment{EnvironmentID: env, Enabled: true, Rules: &[]client.FlagRule{}}
	for _, e := range f.Environments {
		if e.EnvironmentID == env {
			current = e
		}
	}
	imported, diags := req.Private.GetKey(ctx, privateImported)
	resp.Diagnostics.Append(diags...)
	state.fromAPI(current, len(imported) > 0, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(imported) > 0 {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, privateImported, nil)...)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *flagEnvResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state flagEnvModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !r.set(ctx, &plan, &state, &resp.Diagnostics, "update") {
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
	e, err := r.client.SetFeatureFlagEnvironment(ctx, pid, key, env, client.SetFeatureFlagEnvironmentRequest{Enabled: true})
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to clear flag %q in environment %d of project %d", key, env, pid), err, flagPermissions)
		return
	}
	// Rules in state are managed here: remove them too.
	if len(state.Rules) > 0 && e.Rules != nil && len(*e.Rules) > 0 {
		if _, err := r.client.SetFeatureFlagRules(ctx, pid, key, env, nil, e.UpdatedAt); err != nil && !client.IsNotFound(err) {
			addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to remove the targeting rules of flag %q in environment %d of project %d", key, env, pid), err, flagPermissions)
		}
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
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, privateImported, []byte("true"))...)
}
