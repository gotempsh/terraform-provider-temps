// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var errorAlertRulePermissions = []string{"error_tracking:read", "error_tracking:create", "error_tracking:write"}

// errorAlertTriggers maps each trigger type to the trigger_config keys it needs.
var errorAlertTriggers = map[string][]string{
	"new_issue":     nil,
	"regression":    nil,
	"status_change": nil,
	"new_user":      nil,
	"frequency":     {"count", "window_minutes"},
	"user_count":    {"threshold"},
}

var (
	_ resource.Resource                   = &errorAlertRuleResource{}
	_ resource.ResourceWithImportState    = &errorAlertRuleResource{}
	_ resource.ResourceWithValidateConfig = &errorAlertRuleResource{}
)

type errorAlertRuleResource struct{ resourceBase }

func newErrorAlertRuleResource() resource.Resource { return &errorAlertRuleResource{} }

type errorAlertRuleModel struct {
	ID              types.String `tfsdk:"id"`
	ProjectID       types.Int64  `tfsdk:"project_id"`
	Name            types.String `tfsdk:"name"`
	TriggerType     types.String `tfsdk:"trigger_type"`
	TriggerConfig   types.String `tfsdk:"trigger_config"`
	EnvironmentID   types.Int64  `tfsdk:"environment_id"`
	ErrorType       types.String `tfsdk:"error_type"`
	Priority        types.String `tfsdk:"priority"`
	CooldownMinutes types.Int64  `tfsdk:"cooldown_minutes"`
	Enabled         types.Bool   `tfsdk:"enabled"`
}

func (r *errorAlertRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_error_alert_rule"
}

func (r *errorAlertRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	triggers := make([]string, 0, len(errorAlertTriggers))
	for t := range errorAlertTriggers {
		triggers = append(triggers, t)
	}
	slices.Sort(triggers)
	resp.Schema = schema.Schema{
		MarkdownDescription: "Notifies when a project's error tracking sees a new issue, a regression, a burst and more.\n\n" +
			"Temps creates two rules for every project (*New issue detected* and *Regression detected*); import them to " +
			"manage them, or leave them alone.\n\n" +
			"Temps cannot remove `environment_id` or `error_type` from a rule, so removing either replaces the rule.\n\n" +
			"Import with `<project_id>/<rule_id>`.\n\n" +
			"Required API key permissions: " + permList(errorAlertRulePermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric rule ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the project. Changing it replaces the rule.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Rule name.",
				Validators: []validator.String{notBlank{}}},
			"trigger_type": schema.StringAttribute{Required: true,
				MarkdownDescription: "What fires the rule: one of `" + joinTicks(triggers) + "`.",
				Validators:          []validator.String{stringvalidator.OneOf(triggers...)}},
			"trigger_config": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("{}"),
				MarkdownDescription: "Trigger settings as JSON. `frequency` needs `count` and `window_minutes` " +
					"(`jsonencode({ count = 100, window_minutes = 60 })`); `user_count` needs `threshold`; the others take `{}`.",
				Validators: []validator.String{jsonString{}}},
			"environment_id": schema.Int64Attribute{Optional: true,
				MarkdownDescription: "Only fire for errors in this environment of the project. Removing it replaces the rule.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplaceIf(removedInt64,
					"Temps cannot remove the environment filter in place.", "Temps cannot remove the environment filter in place.")}},
			"error_type": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Only fire for errors of this type, compared without case, for example `TypeError`. " +
					"Removing it replaces the rule.",
				Validators:    []validator.String{notBlank{}},
				PlanModifiers: []planmodifier.String{stringRequiresReplaceWhenRemoved("Temps cannot remove the error type filter in place.")}},
			"priority": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("High"),
				MarkdownDescription: "Notification priority: `Low`, `Normal`, `High` (the default) or `Critical`.",
				Validators:          []validator.String{stringvalidator.OneOf("Low", "Normal", "High", "Critical")}},
			"cooldown_minutes": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(30),
				MarkdownDescription: "Minimum minutes between notifications for the same rule and error. Defaults to `30`.",
				Validators:          []validator.Int64{int64validator.Between(0, 1<<31-1)}},
			"enabled": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "Whether the rule fires. Defaults to `true`."},
		},
	}
}

func removedInt64(_ context.Context, req planmodifier.Int64Request, resp *int64planmodifier.RequiresReplaceIfFuncResponse) {
	resp.RequiresReplace = !req.StateValue.IsNull() && req.PlanValue.IsNull()
}

func (r *errorAlertRuleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg errorAlertRuleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || !known(cfg.TriggerType) || cfg.TriggerConfig.IsUnknown() {
		return
	}
	raw := "{}" // the default when unset
	if !cfg.TriggerConfig.IsNull() {
		raw = cfg.TriggerConfig.ValueString()
	}
	var conf map[string]any
	if err := json.Unmarshal([]byte(raw), &conf); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("trigger_config"), "Invalid trigger_config",
			"trigger_config must be a JSON object, for example jsonencode({ count = 100, window_minutes = 60 }).")
		return
	}
	for _, key := range errorAlertTriggers[cfg.TriggerType.ValueString()] {
		n, ok := conf[key].(float64)
		if !ok || n < 1 {
			resp.Diagnostics.AddAttributeError(path.Root("trigger_config"), "Invalid trigger_config",
				fmt.Sprintf("trigger_type %q needs trigger_config.%s, a number of at least 1.", cfg.TriggerType.ValueString(), key))
		}
	}
}

func (m *errorAlertRuleModel) fromAPI(r *client.ErrorAlertRule) {
	m.ID = types.StringValue(formatID(r.ID))
	m.ProjectID = types.Int64Value(r.ProjectID)
	m.Name = types.StringValue(r.Name)
	m.TriggerType = types.StringValue(r.TriggerType)
	m.TriggerConfig = jsonValue(m.TriggerConfig, r.TriggerConfig)
	if m.TriggerConfig.IsNull() {
		m.TriggerConfig = types.StringValue("{}")
	}
	m.EnvironmentID = types.Int64PointerValue(r.EnvironmentFilter)
	m.ErrorType = types.StringNull()
	if r.ErrorLevelFilter != nil && *r.ErrorLevelFilter != "" {
		m.ErrorType = types.StringValue(*r.ErrorLevelFilter)
	}
	m.Priority = types.StringValue(r.NotificationPriority)
	m.CooldownMinutes = types.Int64Value(r.CooldownMinutes)
	m.Enabled = types.BoolValue(r.Enabled)
}

func (m *errorAlertRuleModel) request() client.ErrorAlertRuleRequest {
	return client.ErrorAlertRuleRequest{
		Name: m.Name.ValueString(), TriggerType: m.TriggerType.ValueString(),
		TriggerConfig:     json.RawMessage(m.TriggerConfig.ValueString()),
		EnvironmentFilter: int64Ptr(m.EnvironmentID), ErrorLevelFilter: strPtr(m.ErrorType),
		NotificationPriority: m.Priority.ValueString(), CooldownMinutes: m.CooldownMinutes.ValueInt64(),
		Enabled: m.Enabled.ValueBool(),
	}
}

func (r *errorAlertRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan errorAlertRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid := plan.ProjectID.ValueInt64()
	rule, err := r.client.CreateErrorAlertRule(ctx, pid, plan.request())
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create error alert rule "+plan.Name.ValueString()+" in project "+formatID(pid), err, errorAlertRulePermissions)
		return
	}
	plan.fromAPI(rule)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *errorAlertRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state errorAlertRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_error_alert_rule ID", err.Error())
		return
	}
	rule, err := r.client.GetErrorAlertRule(ctx, state.ProjectID.ValueInt64(), id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read error alert rule "+formatID(id), err, errorAlertRulePermissions)
		return
	}
	state.fromAPI(rule)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *errorAlertRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state errorAlertRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_error_alert_rule ID", err.Error())
		return
	}
	rule, err := r.client.UpdateErrorAlertRule(ctx, plan.ProjectID.ValueInt64(), id, plan.request())
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update error alert rule "+formatID(id), err, errorAlertRulePermissions)
		return
	}
	plan.fromAPI(rule)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *errorAlertRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state errorAlertRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_error_alert_rule ID", err.Error())
		return
	}
	if err := r.client.DeleteErrorAlertRule(ctx, state.ProjectID.ValueInt64(), id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete error alert rule "+formatID(id), err, errorAlertRulePermissions)
	}
}

func (r *errorAlertRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	pid, id, err := parseCompositeID(req.ID, "<project_id>/<rule_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), pid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), formatID(id))...)
}
