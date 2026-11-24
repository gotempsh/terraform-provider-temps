// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var serviceLinkPermissions = []string{"external_services:read", "external_services:write", "projects:read"}

var (
	_ resource.Resource                = &serviceLinkResource{}
	_ resource.ResourceWithImportState = &serviceLinkResource{}
)

type serviceLinkResource struct{ resourceBase }

func newServiceLinkResource() resource.Resource { return &serviceLinkResource{} }

type serviceLinkModel struct {
	ID                       types.String `tfsdk:"id"`
	ServiceID                types.Int64  `tfsdk:"service_id"`
	ProjectID                types.Int64  `tfsdk:"project_id"`
	DatabaseProvisioningMode types.String `tfsdk:"database_provisioning_mode"`
	CustomDatabaseName       types.String `tfsdk:"custom_database_name"`
}

func (r *serviceLinkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_link"
}

func (r *serviceLinkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceInt := []planmodifier.Int64{int64planmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Links a managed service to a project so its deployments receive the service's connection " +
			"variables (for PostgreSQL: `POSTGRES_URL`, `POSTGRES_HOST`, ...). Every change replaces the link.\n\n" +
			"Import with `<service_id>/<project_id>`.\n\nRequired API key permissions: " + permList(serviceLinkPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric link ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"service_id": schema.Int64Attribute{Required: true, PlanModifiers: replaceInt, MarkdownDescription: "ID of the service."},
			"project_id": schema.Int64Attribute{Required: true, PlanModifiers: replaceInt, MarkdownDescription: "ID of the project."},
			"database_provisioning_mode": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "Which logical database deployments receive: `project` (one per project, the default), " +
					"`project_environment` (one per environment) or `custom` (`custom_database_name`).",
				Validators: []validator.String{stringvalidator.OneOf("project", "project_environment", "custom")},
			},
			"custom_database_name": schema.StringAttribute{
				Optional: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "Database name when `database_provisioning_mode` is `custom`.",
			},
		},
	}
}

func (m *serviceLinkModel) fromAPI(l *client.ServiceLink, serviceID int64) {
	m.ID = types.StringValue(formatID(l.ID))
	m.ServiceID = types.Int64Value(serviceID)
	m.ProjectID = types.Int64Value(l.Project.ID)
	m.DatabaseProvisioningMode = types.StringValue(l.DatabaseProvisioningMode)
	m.CustomDatabaseName = types.StringPointerValue(l.CustomDatabaseName)
}

func (r *serviceLinkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serviceLinkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	sid, pid := plan.ServiceID.ValueInt64(), plan.ProjectID.ValueInt64()
	if plan.DatabaseProvisioningMode.ValueString() == "custom" && !known(plan.CustomDatabaseName) {
		resp.Diagnostics.AddAttributeError(path.Root("custom_database_name"), "custom_database_name is required",
			"database_provisioning_mode = \"custom\" needs custom_database_name.")
		return
	}
	l, err := r.client.LinkService(ctx, sid, client.LinkServiceRequest{
		ProjectID: pid, DatabaseProvisioningMode: strPtr(plan.DatabaseProvisioningMode), CustomDatabaseName: strPtr(plan.CustomDatabaseName),
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to link Temps service "+formatID(sid)+" to project "+formatID(pid), err, serviceLinkPermissions)
		return
	}
	plan.fromAPI(l, sid)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceLinkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serviceLinkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	sid, pid := state.ServiceID.ValueInt64(), state.ProjectID.ValueInt64()
	l, err := r.client.GetServiceLink(ctx, sid, pid)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the link between Temps service "+formatID(sid)+" and project "+formatID(pid), err, serviceLinkPermissions)
		return
	}
	state.fromAPI(l, sid)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: every attribute forces replacement.
func (r *serviceLinkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("temps_service_link cannot be updated in place", "Every attribute change replaces the link; this is a provider bug.")
}

func (r *serviceLinkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serviceLinkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	sid, pid := state.ServiceID.ValueInt64(), state.ProjectID.ValueInt64()
	if err := r.client.UnlinkService(ctx, sid, pid); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to unlink Temps service "+formatID(sid)+" from project "+formatID(pid), err, serviceLinkPermissions)
	}
}

func (r *serviceLinkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	sid, pid, err := parseCompositeID(req.ID, "<service_id>/<project_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("service_id"), sid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), pid)...)
}
