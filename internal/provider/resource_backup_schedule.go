// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var backupSchedulePermissions = []string{"backups:create", "backups:read", "backups:write", "backups:delete"}

var (
	_ resource.Resource                = &backupScheduleResource{}
	_ resource.ResourceWithImportState = &backupScheduleResource{}
)

type backupScheduleResource struct{ resourceBase }

func newBackupScheduleResource() resource.Resource { return &backupScheduleResource{} }

type backupScheduleModel struct {
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	Description         types.String `tfsdk:"description"`
	BackupType          types.String `tfsdk:"backup_type"`
	ScheduleExpression  types.String `tfsdk:"schedule_expression"`
	RetentionPeriod     types.Int64  `tfsdk:"retention_period"`
	S3SourceID          types.Int64  `tfsdk:"s3_source_id"`
	ServiceIDs          types.Set    `tfsdk:"service_ids"`
	TargetAllServices   types.Bool   `tfsdk:"target_all_services"`
	IncludeControlPlane types.Bool   `tfsdk:"include_control_plane"`
	Tags                types.List   `tfsdk:"tags"`
	Enabled             types.Bool   `tfsdk:"enabled"`
	MaxRuntimeSecs      types.Int64  `tfsdk:"max_runtime_secs"`
}

// cronFields validates the seconds-first cron syntax Temps uses
// ("sec min hour day-of-month month day-of-week [year]").
type cronFields struct{}

func (cronFields) Description(context.Context) string {
	return "must be a 6- or 7-field cron expression starting with seconds"
}
func (v cronFields) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (cronFields) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !known(req.ConfigValue) {
		return
	}
	n := len(strings.Fields(req.ConfigValue.ValueString()))
	if n == 6 || n == 7 {
		return
	}
	hint := ""
	if n == 5 {
		hint = ` Temps cron expressions start with a seconds field: "0 3 * * *" becomes "0 0 3 * * *".`
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid schedule_expression",
		"Expected 6 or 7 space-separated fields (sec min hour day-of-month month day-of-week [year]), got "+
			formatID(int64(n))+"."+hint)
}

func (r *backupScheduleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_backup_schedule"
}

func (r *backupScheduleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A recurring backup of managed services (and optionally the Temps control plane) to a backup destination.\n\n" +
			"Required API key permissions: " + permList(backupSchedulePermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric schedule ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":        schema.StringAttribute{Required: true, MarkdownDescription: "Schedule name.", Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Free-form description."},
			"backup_type": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("full"),
				MarkdownDescription: "Backup type, `full` by default. Changing it replaces the schedule.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"schedule_expression": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "When to run, as a cron expression **with a leading seconds field**: " +
					"`0 0 3 * * *` runs daily at 03:00 (server time).",
				Validators: []validator.String{cronFields{}},
			},
			"retention_period": schema.Int64Attribute{
				Required: true, MarkdownDescription: "How many days backups are kept.",
				Validators: []validator.Int64{int64validator.AtLeast(1)},
			},
			"s3_source_id": schema.Int64Attribute{
				Optional: true, Computed: true,
				MarkdownDescription: "ID of the `temps_backup_destination` to write to; the default destination when unset. Changing it replaces the schedule.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown(), int64planmodifier.RequiresReplace()},
			},
			"service_ids": schema.SetAttribute{
				Optional: true, Computed: true, ElementType: types.Int64Type,
				MarkdownDescription: "IDs of the services to back up. Conflicts with `target_all_services = true`.",
				PlanModifiers:       []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
				Validators:          []validator.Set{setvalidator.SizeAtLeast(1)},
			},
			"target_all_services": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Back up every service, including ones created later.",
			},
			"include_control_plane": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Also back up the Temps control-plane database.",
			},
			"tags": schema.ListAttribute{
				Optional: true, Computed: true, ElementType: types.StringType,
				Default:             listdefault.StaticValue(types.ListValueMust(types.StringType, nil)),
				MarkdownDescription: "Tags attached to every backup the schedule takes.",
			},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "Whether the schedule runs.",
			},
			"max_runtime_secs": schema.Int64Attribute{
				Optional: true, MarkdownDescription: "Abort a run that takes longer than this many seconds.",
				Validators: []validator.Int64{int64validator.AtLeast(1)},
			},
		},
	}
}

func (m *backupScheduleModel) fromAPI(s *client.BackupSchedule, serviceIDs []int64) {
	m.ID = types.StringValue(formatID(s.ID))
	m.Name = types.StringValue(s.Name)
	m.Description = types.StringPointerValue(s.Description)
	if s.Description != nil && *s.Description == "" {
		m.Description = types.StringNull() // cleared
	}
	m.BackupType = types.StringValue(s.BackupType)
	m.ScheduleExpression = types.StringValue(s.ScheduleExpression)
	m.RetentionPeriod = types.Int64Value(s.RetentionPeriod)
	m.S3SourceID = types.Int64Value(s.S3SourceID)
	m.TargetAllServices = types.BoolValue(s.TargetAllServices)
	m.IncludeControlPlane = types.BoolValue(s.IncludeControlPlane)
	tags := s.Tags
	if tags == nil {
		tags = []string{}
	}
	m.Tags = stringListValue(tags)
	m.Enabled = types.BoolValue(s.Enabled)
	m.MaxRuntimeSecs = types.Int64PointerValue(s.MaxRuntimeSecs)
	if len(serviceIDs) == 0 && s.TargetAllServices {
		m.ServiceIDs = types.SetNull(types.Int64Type)
	} else {
		slices.Sort(serviceIDs)
		m.ServiceIDs = int64SetValue(serviceIDs)
	}
}

func (r *backupScheduleResource) readBack(ctx context.Context, id int64) (*client.BackupSchedule, []int64, error) {
	s, err := r.client.GetBackupSchedule(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	ids, err := r.client.ListBackupScheduleServices(ctx, id)
	return s, ids, err
}

func (r *backupScheduleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan backupScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.TargetAllServices.ValueBool() && known(plan.ServiceIDs) {
		resp.Diagnostics.AddAttributeError(path.Root("service_ids"), "Conflicting arguments",
			"service_ids cannot be combined with target_all_services = true.")
		return
	}
	serviceIDs := int64sFromSet(ctx, plan.ServiceIDs, &resp.Diagnostics)
	tags := stringsFromList(ctx, plan.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if serviceIDs == nil {
		serviceIDs = []int64{}
	}
	if tags == nil {
		tags = []string{}
	}
	s, err := r.client.CreateBackupSchedule(ctx, client.CreateBackupScheduleRequest{
		Name: plan.Name.ValueString(), Description: strPtr(plan.Description), BackupType: plan.BackupType.ValueString(),
		RetentionPeriod: plan.RetentionPeriod.ValueInt64(), S3SourceID: int64Ptr(plan.S3SourceID),
		ScheduleExpression: plan.ScheduleExpression.ValueString(), Enabled: plan.Enabled.ValueBool(), Tags: tags,
		ServiceIDs: serviceIDs, TargetAllServices: boolPtr(plan.TargetAllServices),
		IncludeControlPlane: boolPtr(plan.IncludeControlPlane), MaxRuntimeSecs: int64Ptr(plan.MaxRuntimeSecs),
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create Temps backup schedule "+plan.Name.String(), err, backupSchedulePermissions)
		return
	}
	s, ids, err := r.readBack(ctx, s.ID)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read Temps backup schedule after create", err, backupSchedulePermissions)
		return
	}
	plan.fromAPI(s, ids)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *backupScheduleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state backupScheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_backup_schedule ID", err.Error())
		return
	}
	s, ids, err := r.readBack(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read Temps backup schedule "+formatID(id), err, backupSchedulePermissions)
		return
	}
	state.fromAPI(s, ids)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *backupScheduleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state backupScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_backup_schedule ID", err.Error())
		return
	}
	if plan.TargetAllServices.ValueBool() && known(plan.ServiceIDs) && len(plan.ServiceIDs.Elements()) > 0 && !plan.ServiceIDs.Equal(state.ServiceIDs) {
		resp.Diagnostics.AddAttributeError(path.Root("service_ids"), "Conflicting arguments",
			"service_ids cannot be combined with target_all_services = true.")
		return
	}
	upd := client.UpdateBackupScheduleRequest{
		Name: strPtr(plan.Name), RetentionPeriod: int64Ptr(plan.RetentionPeriod),
		ScheduleExpression: strPtr(plan.ScheduleExpression), Enabled: boolPtr(plan.Enabled),
		TargetAllServices: boolPtr(plan.TargetAllServices), IncludeControlPlane: boolPtr(plan.IncludeControlPlane),
		MaxRuntimeSecs: int64Ptr(plan.MaxRuntimeSecs),
	}
	desc := plan.Description.ValueString() // "" clears it
	upd.Description = &desc
	tags := stringsFromList(ctx, plan.Tags, &resp.Diagnostics)
	if tags == nil {
		tags = []string{}
	}
	upd.Tags = &tags
	if known(plan.ServiceIDs) && !plan.ServiceIDs.Equal(state.ServiceIDs) {
		ids := int64sFromSet(ctx, plan.ServiceIDs, &resp.Diagnostics)
		upd.ServiceIDs = &ids
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.client.UpdateBackupSchedule(ctx, id, upd); err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update Temps backup schedule "+formatID(id), err, backupSchedulePermissions)
		return
	}
	s, ids, err := r.readBack(ctx, id)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read Temps backup schedule "+formatID(id)+" after update", err, backupSchedulePermissions)
		return
	}
	// The PATCH body has no "enabled" toggle on older servers; enforce it.
	if s.Enabled != plan.Enabled.ValueBool() {
		if err := r.client.SetBackupScheduleEnabled(ctx, id, plan.Enabled.ValueBool()); err != nil {
			addAPIError(&resp.Diagnostics, "Unable to enable or disable Temps backup schedule "+formatID(id), err, backupSchedulePermissions)
			return
		}
		s.Enabled = plan.Enabled.ValueBool()
	}
	plan.fromAPI(s, ids)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *backupScheduleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state backupScheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_backup_schedule ID", err.Error())
		return
	}
	if err := r.client.DeleteBackupSchedule(ctx, id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete Temps backup schedule "+formatID(id), err, backupSchedulePermissions)
	}
}

func (r *backupScheduleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := parseID(req.ID); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
