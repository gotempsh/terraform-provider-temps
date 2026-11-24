// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var monitorPermissions = []string{"status_page:create", "status_page:read", "status_page:write", "status_page:delete", "projects:write"}

var (
	_ resource.Resource                = &monitorResource{}
	_ resource.ResourceWithImportState = &monitorResource{}
)

type monitorResource struct{ resourceBase }

func newMonitorResource() resource.Resource { return &monitorResource{} }

type monitorModel struct {
	ID                   types.String `tfsdk:"id"`
	ProjectID            types.Int64  `tfsdk:"project_id"`
	EnvironmentID        types.Int64  `tfsdk:"environment_id"`
	Name                 types.String `tfsdk:"name"`
	MonitorType          types.String `tfsdk:"monitor_type"`
	CheckPath            types.String `tfsdk:"check_path"`
	CheckIntervalSeconds types.Int64  `tfsdk:"check_interval_seconds"`
	MonitorURL           types.String `tfsdk:"monitor_url"`
}

// checkPath mirrors the server's check_path validation so mistakes fail at plan time.
type checkPath struct{}

func (checkPath) Description(context.Context) string {
	return "must start with / and contain no query, fragment, scheme or userinfo"
}
func (v checkPath) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (checkPath) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !known(req.ConfigValue) {
		return
	}
	p := req.ConfigValue.ValueString()
	var problem string
	switch {
	case !strings.HasPrefix(p, "/"):
		problem = "must start with '/'"
	case strings.ContainsAny(p, "?#"):
		problem = "must not contain a query string or fragment"
	case strings.Contains(p, "@"):
		problem = "must not contain '@'"
	case strings.Contains(p, "://"):
		problem = "must be a path, not a URL"
	case len(p) > 2048:
		problem = "must be at most 2048 bytes"
	}
	if problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid check_path", "check_path "+problem+", got "+req.ConfigValue.String()+".")
	}
}

func (r *monitorResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_monitor"
}

func (r *monitorResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "An uptime monitor that probes a project environment's URL. Show it on a `temps_status_page`.\n\n" +
			"Only `check_path` can change in place; the API cannot update the other attributes, so changing them replaces the monitor.\n\n" +
			"Required API key permissions: " + permList(monitorPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric monitor ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the project.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"environment_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the environment to probe; it must belong to `project_id`.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"name": schema.StringAttribute{Required: true, PlanModifiers: replaceStr, MarkdownDescription: "Monitor name.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"monitor_type": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("web"), PlanModifiers: replaceStr,
				MarkdownDescription: "`web` probes the environment URL (plus `check_path`); `health` probes `/health` unless `check_path` is set.",
				Validators:          []validator.String{stringvalidator.OneOf("web", "health")},
			},
			"check_path": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Path to probe, for example `/healthz`.",
				Validators:          []validator.String{checkPath{}},
			},
			"check_interval_seconds": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(60),
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
				MarkdownDescription: "Seconds between checks, at least 30 (the server raises lower values to 30, " +
					"so they are rejected here to avoid a permanent diff). Changing it replaces the monitor.",
				Validators: []validator.Int64{int64validator.Between(30, 86400)},
			},
			"monitor_url": schema.StringAttribute{Computed: true, MarkdownDescription: "Full URL being probed."},
		},
	}
}

func (m *monitorModel) fromAPI(mon *client.Monitor) {
	m.ID = types.StringValue(formatID(mon.ID))
	m.ProjectID = types.Int64Value(mon.ProjectID)
	m.EnvironmentID = types.Int64PointerValue(mon.EnvironmentID)
	m.Name = types.StringValue(mon.Name)
	m.MonitorType = types.StringValue(mon.MonitorType)
	m.CheckPath = types.StringPointerValue(mon.CheckPath)
	m.CheckIntervalSeconds = types.Int64Value(mon.CheckIntervalSeconds)
	m.MonitorURL = types.StringValue(mon.MonitorURL)
}

func (r *monitorResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan monitorModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid := plan.ProjectID.ValueInt64()
	m, err := r.client.CreateMonitor(ctx, pid, client.CreateMonitorRequest{
		Name: plan.Name.ValueString(), MonitorType: plan.MonitorType.ValueString(), EnvironmentID: plan.EnvironmentID.ValueInt64(),
		CheckPath: strPtr(plan.CheckPath), CheckIntervalSeconds: int64Ptr(plan.CheckIntervalSeconds),
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create Temps monitor "+plan.Name.String()+" in project "+formatID(pid), err, monitorPermissions)
		return
	}
	plan.fromAPI(m)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *monitorResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state monitorModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_monitor ID", err.Error())
		return
	}
	m, err := r.client.GetMonitor(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read Temps monitor "+formatID(id), err, monitorPermissions)
		return
	}
	state.fromAPI(m)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *monitorResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state monitorModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_monitor ID", err.Error())
		return
	}
	m, err := r.client.GetMonitor(ctx, id)
	if known(plan.CheckPath) && !plan.CheckPath.Equal(state.CheckPath) {
		m, err = r.client.UpdateMonitorCheckPath(ctx, id, plan.CheckPath.ValueString())
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update Temps monitor "+formatID(id), err, monitorPermissions)
		return
	}
	plan.fromAPI(m)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *monitorResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state monitorModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_monitor ID", err.Error())
		return
	}
	if err := r.client.DeleteMonitor(ctx, id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete Temps monitor "+formatID(id), err, monitorPermissions)
	}
}

func (r *monitorResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := parseID(req.ID); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
