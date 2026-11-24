// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
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

var customDomainPermissions = []string{"projects:read", "projects:write", "projects:delete"}

var (
	_ resource.Resource                = &customDomainResource{}
	_ resource.ResourceWithImportState = &customDomainResource{}
)

type customDomainResource struct{ resourceBase }

func newCustomDomainResource() resource.Resource { return &customDomainResource{} }

type customDomainModel struct {
	ID            types.String `tfsdk:"id"`
	ProjectID     types.Int64  `tfsdk:"project_id"`
	EnvironmentID types.Int64  `tfsdk:"environment_id"`
	Domain        types.String `tfsdk:"domain"`
	RedirectTo    types.String `tfsdk:"redirect_to"`
	StatusCode    types.Int64  `tfsdk:"status_code"`
	Branch        types.String `tfsdk:"branch"`
	ServiceName   types.String `tfsdk:"service_name"`
	Status        types.String `tfsdk:"status"`
	Message       types.String `tfsdk:"message"`
}

// requiresReplaceWhenRemoved replaces the resource when an optional value is
// removed from configuration, for attributes the API can set but not clear.
func requiresReplaceWhenRemoved(description string) planmodifier.Int64 {
	return int64planmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.Int64Request, resp *int64planmodifier.RequiresReplaceIfFuncResponse) {
		resp.RequiresReplace = !req.StateValue.IsNull() && req.PlanValue.IsNull()
	}, description, description)
}

// stringRequiresReplaceWhenRemoved is requiresReplaceWhenRemoved for strings.
func stringRequiresReplaceWhenRemoved(description string) planmodifier.String {
	return stringplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
		resp.RequiresReplace = !req.StateValue.IsNull() && req.PlanValue.IsNull()
	}, description, description)
}

func (r *customDomainResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_custom_domain"
}

func (r *customDomainResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A custom hostname that routes to a project environment, or redirects to another URL.\n\n" +
			"Temps requests the TLS certificate once DNS points at the server; `status` shows progress. " +
			"Domains are unique across the whole server, not only within a project.\n\n" +
			"Import with `<project_id>/<domain_id>`.\n\nRequired API key permissions: " + permList(customDomainPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric custom domain ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the project.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"environment_id": schema.Int64Attribute{Required: true,
				MarkdownDescription: "ID of the environment the domain serves; it must belong to `project_id`. Changes in place."},
			"domain": schema.StringAttribute{Required: true,
				MarkdownDescription: "Hostname, lower case without a trailing dot, for example `app.example.com` or `*.preview.example.com`. Changes in place.",
				Validators: []validator.String{stringvalidator.RegexMatches(hostnameRE,
					"must be a lower-case hostname such as app.example.com (Temps lower-cases names and strips a trailing dot, so other spellings would show a permanent diff)")}},
			"redirect_to": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Redirect every request to this URL instead of serving the environment.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)}},
			"status_code": schema.Int64Attribute{Optional: true,
				MarkdownDescription: "HTTP status of the redirect: 301, 302, 303, 307 or 308. Requires `redirect_to`. " +
					"The API cannot clear it, so removing it replaces the domain.",
				Validators: []validator.Int64{
					int64validator.OneOf(redirectCode...),
					int64validator.AlsoRequires(path.MatchRoot("redirect_to")),
				},
				PlanModifiers: []planmodifier.Int64{requiresReplaceWhenRemoved("Temps cannot clear a redirect status code in place.")}},
			"branch": schema.StringAttribute{Optional: true, MarkdownDescription: "Git branch whose preview this domain serves.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"service_name": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Docker Compose service the domain routes to. Only meaningful for Docker Compose projects.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)}},
			"status":  schema.StringAttribute{Computed: true, MarkdownDescription: "Certificate status, for example `pending` or `active`."},
			"message": schema.StringAttribute{Computed: true, MarkdownDescription: "Detail about the status, such as why a certificate failed."},
		},
	}
}

func (m *customDomainModel) fromAPI(d *client.CustomDomain) {
	m.ID = types.StringValue(formatID(d.ID))
	m.ProjectID = types.Int64Value(d.ProjectID)
	if id := d.EnvironmentID(); id != 0 {
		m.EnvironmentID = types.Int64Value(id)
	}
	m.Domain = types.StringValue(d.Domain)
	m.RedirectTo = types.StringPointerValue(d.RedirectTo)
	m.StatusCode = types.Int64PointerValue(d.StatusCode)
	m.Branch = types.StringPointerValue(d.Branch)
	m.ServiceName = types.StringPointerValue(d.ServiceName)
	m.Status = types.StringValue(d.Status)
	m.Message = types.StringPointerValue(d.Message)
}

func (r *customDomainResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan customDomainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid := plan.ProjectID.ValueInt64()
	d, err := r.client.CreateCustomDomain(ctx, pid, client.CustomDomainRequest{
		Domain: strPtr(plan.Domain), EnvironmentID: int64Ptr(plan.EnvironmentID), RedirectTo: strPtr(plan.RedirectTo),
		StatusCode: int64Ptr(plan.StatusCode), Branch: strPtr(plan.Branch), ServiceName: strPtr(plan.ServiceName),
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to add custom domain "+plan.Domain.String()+" to project "+formatID(pid), err, customDomainPermissions)
		return
	}
	plan.fromAPI(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *customDomainResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state customDomainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_custom_domain ID", err.Error())
		return
	}
	d, err := r.client.GetCustomDomain(ctx, state.ProjectID.ValueInt64(), id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read custom domain "+formatID(id), err, customDomainPermissions)
		return
	}
	state.fromAPI(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// clearable sends the planned value, or "" to clear one that was removed.
func clearable(plan, state types.String) *string {
	if known(plan) {
		return strPtr(plan)
	}
	if !state.IsNull() {
		return ptr("")
	}
	return nil
}

func (r *customDomainResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state customDomainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_custom_domain ID", err.Error())
		return
	}
	d, err := r.client.UpdateCustomDomain(ctx, plan.ProjectID.ValueInt64(), id, client.CustomDomainRequest{
		Domain: strPtr(plan.Domain), EnvironmentID: int64Ptr(plan.EnvironmentID),
		RedirectTo: clearable(plan.RedirectTo, state.RedirectTo), StatusCode: int64Ptr(plan.StatusCode),
		Branch: clearable(plan.Branch, state.Branch), ServiceName: clearable(plan.ServiceName, state.ServiceName),
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update custom domain "+formatID(id), err, customDomainPermissions)
		return
	}
	plan.fromAPI(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *customDomainResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state customDomainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_custom_domain ID", err.Error())
		return
	}
	if err := r.client.DeleteCustomDomain(ctx, state.ProjectID.ValueInt64(), id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete custom domain "+formatID(id), err, customDomainPermissions)
	}
}

func (r *customDomainResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	pid, id, err := parseCompositeID(req.ID, "<project_id>/<domain_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), pid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), formatID(id))...)
}
