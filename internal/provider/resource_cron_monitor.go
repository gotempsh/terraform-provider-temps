// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/objectvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
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

// Cron monitor endpoints are guarded by the environments permissions.
var cronMonitorPermissions = []string{"environments:read", "environments:write"}

var (
	_ resource.Resource                   = &cronMonitorResource{}
	_ resource.ResourceWithImportState    = &cronMonitorResource{}
	_ resource.ResourceWithValidateConfig = &cronMonitorResource{}
)

type cronMonitorResource struct{ resourceBase }

func newCronMonitorResource() resource.Resource { return &cronMonitorResource{} }

type cronMonitorModel struct {
	ID                    types.String `tfsdk:"id"`
	ProjectID             types.Int64  `tfsdk:"project_id"`
	EnvironmentID         types.Int64  `tfsdk:"environment_id"`
	Slug                  types.String `tfsdk:"slug"`
	Schedule              types.Object `tfsdk:"schedule"`
	Timezone              types.String `tfsdk:"timezone"`
	CheckinMargin         types.Int64  `tfsdk:"checkin_margin"`
	MaxRuntime            types.Int64  `tfsdk:"max_runtime"`
	FailureIssueThreshold types.Int64  `tfsdk:"failure_issue_threshold"`
	RecoveryThreshold     types.Int64  `tfsdk:"recovery_threshold"`
	Enabled               types.Bool   `tfsdk:"enabled"`
	Status                types.String `tfsdk:"status"`
}

type cronScheduleModel struct {
	Crontab  types.String `tfsdk:"crontab"`
	Interval types.Int64  `tfsdk:"interval"`
	Unit     types.String `tfsdk:"unit"`
}

var cronScheduleAttrTypes = map[string]attr.Type{"crontab": types.StringType, "interval": types.Int64Type, "unit": types.StringType}

var cronUnitMinutes = map[string]int64{"minute": 1, "hour": 60, "day": 1440, "week": 10080}

// maxCronIntervalMinutes is the longest interval Temps accepts (one year).
const maxCronIntervalMinutes = 525600

func (r *cronMonitorResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cron_monitor"
}

func (r *cronMonitorResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	limit := func(max int64) []validator.Int64 { return []validator.Int64{int64validator.Between(1, max)} }
	resp.Schema = schema.Schema{
		MarkdownDescription: "Alerts when a scheduled job stops checking in. Jobs check in with the Sentry-compatible cron API " +
			"using `slug`; a monitor defined here makes Temps authoritative over the configuration jobs send.\n\n" +
			"Import with `<project_id>/<monitor_id>`.\n\nRequired API key permissions: " + permList(cronMonitorPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric cron monitor ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the project.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"environment_id": schema.Int64Attribute{Optional: true,
				MarkdownDescription: "Restrict the monitor to one environment of `project_id`. The same slug may exist once project-wide and once per environment.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"slug": schema.StringAttribute{Required: true,
				MarkdownDescription: "Identifier jobs check in with: 1 to 128 letters, digits, `_` or `-`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.RegexMatches(cronSlugRE, "must be 1 to 128 letters, digits, '_' or '-'")}},
			"schedule": schema.SingleNestedAttribute{Required: true,
				MarkdownDescription: "When the job runs: set `crontab`, or `interval` with `unit`.",
				Attributes: map[string]schema.Attribute{
					"crontab": schema.StringAttribute{Optional: true,
						MarkdownDescription: "Five-field crontab expression (minute hour day month weekday), for example `0 3 * * *`, evaluated in `timezone`.",
						Validators: []validator.String{
							stringvalidator.LengthBetween(1, 128),
							stringvalidator.ExactlyOneOf(path.MatchRelative().AtParent().AtName("interval")),
						}},
					"interval": schema.Int64Attribute{Optional: true, MarkdownDescription: "Run every `interval` `unit`s.",
						Validators: []validator.Int64{int64validator.AtLeast(1), int64validator.AlsoRequires(path.MatchRelative().AtParent().AtName("unit"))}},
					"unit": schema.StringAttribute{Optional: true, MarkdownDescription: "`minute`, `hour`, `day` or `week`.",
						Validators: []validator.String{stringvalidator.OneOf("minute", "hour", "day", "week"),
							stringvalidator.AlsoRequires(path.MatchRelative().AtParent().AtName("interval"))}},
				},
				Validators: []validator.Object{objectvalidator.IsRequired()},
			},
			"timezone": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("UTC"),
				MarkdownDescription: "IANA time zone for `crontab`, for example `Europe/Madrid`. Defaults to `UTC`."},
			"checkin_margin": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(1),
				MarkdownDescription: "Minutes a check-in may be late before it counts as missed, 1 to 40320. Defaults to 1.", Validators: limit(40320)},
			"max_runtime": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(30),
				MarkdownDescription: "Minutes a run may take before it counts as failed, 1 to 40320. Defaults to 30.", Validators: limit(40320)},
			"failure_issue_threshold": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(1),
				MarkdownDescription: "Consecutive failures before an alarm opens, 1 to 100. Defaults to 1.", Validators: limit(100)},
			"recovery_threshold": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(1),
				MarkdownDescription: "Consecutive successes before the alarm resolves, 1 to 100. Defaults to 1.", Validators: limit(100)},
			"enabled": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "Whether missed check-ins raise alarms. Defaults to `true`."},
			"status": schema.StringAttribute{Computed: true, MarkdownDescription: "Last known status, for example `ok`, `missed` or `paused`."},
		},
	}
}

func (r *cronMonitorResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg cronMonitorModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if known(cfg.Timezone) {
		if _, err := time.LoadLocation(cfg.Timezone.ValueString()); err != nil || cfg.Timezone.ValueString() == "Local" {
			resp.Diagnostics.AddAttributeError(path.Root("timezone"), "Unknown time zone",
				fmt.Sprintf("timezone %s is not an IANA time zone such as UTC or Europe/Madrid.", cfg.Timezone))
		}
	}
	if !known(cfg.Schedule) {
		return
	}
	var s cronScheduleModel
	resp.Diagnostics.Append(cfg.Schedule.As(ctx, &s, objectAsOptions)...)
	if known(s.Crontab) {
		if n := len(strings.Fields(s.Crontab.ValueString())); n != 5 {
			resp.Diagnostics.AddAttributeError(path.Root("schedule").AtName("crontab"), "Invalid crontab",
				fmt.Sprintf("crontab must have five fields (minute hour day month weekday); %s has %d.", s.Crontab, n))
		}
	}
	if known(s.Interval) && known(s.Unit) {
		if m := s.Interval.ValueInt64() * cronUnitMinutes[s.Unit.ValueString()]; m > maxCronIntervalMinutes {
			resp.Diagnostics.AddAttributeError(path.Root("schedule").AtName("interval"), "Interval too long",
				fmt.Sprintf("%d %ss is %d minutes; Temps accepts at most %d (one year).", s.Interval.ValueInt64(), s.Unit.ValueString(), m, maxCronIntervalMinutes))
		}
	}
}

func (m *cronMonitorModel) config(ctx context.Context) (client.CronMonitorConfig, error) {
	var s cronScheduleModel
	if d := m.Schedule.As(ctx, &s, objectAsOptions); d.HasError() {
		return client.CronMonitorConfig{}, fmt.Errorf("reading schedule: %v", d)
	}
	cfg := client.CronMonitorConfig{
		Timezone: m.Timezone.ValueString(), CheckinMargin: int64Ptr(m.CheckinMargin), MaxRuntime: int64Ptr(m.MaxRuntime),
		FailureIssueThreshold: int64Ptr(m.FailureIssueThreshold), RecoveryThreshold: int64Ptr(m.RecoveryThreshold),
	}
	if known(s.Crontab) {
		cfg.Schedule = client.CronSchedule{Type: "crontab", Value: s.Crontab.ValueString()}
	} else {
		cfg.Schedule = client.CronSchedule{Type: "interval", Value: s.Interval.ValueInt64(), Unit: s.Unit.ValueString()}
	}
	return cfg, nil
}

func (m *cronMonitorModel) fromAPI(c *client.CronMonitor) {
	m.ID = types.StringValue(formatID(c.ID))
	m.ProjectID = types.Int64Value(c.ProjectID)
	m.EnvironmentID = types.Int64PointerValue(c.EnvironmentID)
	m.Slug = types.StringValue(c.Slug)
	m.Enabled = types.BoolValue(c.Enabled)
	m.Status = types.StringValue(c.Status)
	if c.Config == nil {
		return
	}
	sched := map[string]attr.Value{"crontab": types.StringNull(), "interval": types.Int64Null(), "unit": types.StringNull()}
	switch v := c.Config.Schedule.Value.(type) {
	case string:
		sched["crontab"] = types.StringValue(v)
	case float64:
		sched["interval"] = types.Int64Value(int64(v))
		sched["unit"] = types.StringValue(c.Config.Schedule.Unit)
	}
	m.Schedule = types.ObjectValueMust(cronScheduleAttrTypes, sched)
	m.Timezone = types.StringValue(c.Config.Timezone)
	m.CheckinMargin = types.Int64PointerValue(c.Config.CheckinMargin)
	m.MaxRuntime = types.Int64PointerValue(c.Config.MaxRuntime)
	m.FailureIssueThreshold = types.Int64PointerValue(c.Config.FailureIssueThreshold)
	m.RecoveryThreshold = types.Int64PointerValue(c.Config.RecoveryThreshold)
}

func (r *cronMonitorResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan cronMonitorModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	cfg, err := plan.config(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Invalid schedule", err.Error())
		return
	}
	pid := plan.ProjectID.ValueInt64()
	c, err := r.client.CreateCronMonitor(ctx, pid, client.CreateCronMonitorRequest{
		Slug: plan.Slug.ValueString(), EnvironmentID: int64Ptr(plan.EnvironmentID), Config: cfg,
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create cron monitor "+plan.Slug.String()+" in project "+formatID(pid), err, cronMonitorPermissions)
		return
	}
	if !plan.Enabled.ValueBool() {
		if c, err = r.client.UpdateCronMonitor(ctx, pid, c.ID, client.UpdateCronMonitorRequest{Enabled: ptr(false)}); err != nil {
			addAPIError(&resp.Diagnostics, "Created cron monitor "+plan.Slug.String()+" but could not disable it", err, cronMonitorPermissions)
			return
		}
	}
	plan.fromAPI(c)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *cronMonitorResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state cronMonitorModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_cron_monitor ID", err.Error())
		return
	}
	c, err := r.client.GetCronMonitor(ctx, state.ProjectID.ValueInt64(), id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read cron monitor "+formatID(id), err, cronMonitorPermissions)
		return
	}
	state.fromAPI(c)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *cronMonitorResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state cronMonitorModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_cron_monitor ID", err.Error())
		return
	}
	cfg, err := plan.config(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Invalid schedule", err.Error())
		return
	}
	// The server replaces the whole config on PATCH, so every field is sent.
	c, err := r.client.UpdateCronMonitor(ctx, plan.ProjectID.ValueInt64(), id, client.UpdateCronMonitorRequest{
		Config: &cfg, Enabled: boolPtr(plan.Enabled),
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update cron monitor "+formatID(id), err, cronMonitorPermissions)
		return
	}
	plan.fromAPI(c)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *cronMonitorResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state cronMonitorModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_cron_monitor ID", err.Error())
		return
	}
	if err := r.client.DeleteCronMonitor(ctx, state.ProjectID.ValueInt64(), id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete cron monitor "+formatID(id), err, cronMonitorPermissions)
	}
}

func (r *cronMonitorResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	pid, id, err := parseCompositeID(req.ID, "<project_id>/<monitor_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), pid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), formatID(id))...)
}
