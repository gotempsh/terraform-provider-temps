// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"fmt"

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

var (
	teamPermissions          = []string{"users:read", "users:write", "users:delete"}
	projectAccessPermissions = []string{"projects:read", "projects:write"}
)

var (
	_ resource.Resource                = &teamResource{}
	_ resource.ResourceWithImportState = &teamResource{}
	_ resource.Resource                = &projectAccessResource{}
	_ resource.ResourceWithImportState = &projectAccessResource{}
)

type teamResource struct{ resourceBase }

func newTeamResource() resource.Resource { return &teamResource{} }

type teamModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Slug        types.String `tfsdk:"slug"`
	Description types.String `tfsdk:"description"`
}

func (r *teamResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_team"
}

func (r *teamResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A team of users. Grant it a role on projects with `temps_project_access`; members are managed in the console. " +
			"Deleting a team removes its project grants.\n\nImport with the team ID.\n\nRequired API key permissions: " + permList(teamPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric team ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Display name.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"slug": schema.StringAttribute{Required: true,
				MarkdownDescription: "Unique identifier: lower-case letters, digits and `-`. The API cannot rename it, so changing it replaces the team.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.RegexMatches(teamSlugRE, "must contain only lower-case letters, digits and '-'")}},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "What the team is for.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
		},
	}
}

func (m *teamModel) fromAPI(t *client.Team) {
	m.ID = types.StringValue(formatID(t.ID))
	m.Name = types.StringValue(t.Name)
	m.Slug = types.StringValue(t.Slug)
	// The API cannot store a null description once one was set; an empty
	// one is how this provider clears it.
	if t.Description == nil || *t.Description == "" {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue(*t.Description)
	}
}

func (r *teamResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan teamModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := r.client.CreateTeam(ctx, client.TeamRequest{Name: strPtr(plan.Name), Slug: strPtr(plan.Slug), Description: strPtr(plan.Description)})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create team "+plan.Slug.String(), err, teamPermissions)
		return
	}
	plan.fromAPI(t)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *teamResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state teamModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_team ID", err.Error())
		return
	}
	t, err := r.client.GetTeam(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read team "+formatID(id), err, teamPermissions)
		return
	}
	state.fromAPI(t)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *teamResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state teamModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_team ID", err.Error())
		return
	}
	t, err := r.client.UpdateTeam(ctx, id, client.TeamRequest{Name: strPtr(plan.Name), Description: clearable(plan.Description, state.Description)})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update team "+formatID(id), err, teamPermissions)
		return
	}
	plan.fromAPI(t)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *teamResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state teamModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_team ID", err.Error())
		return
	}
	if err := r.client.DeleteTeam(ctx, id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete team "+formatID(id), err, teamPermissions)
	}
}

func (r *teamResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := parseID(req.ID); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

type projectAccessResource struct{ resourceBase }

func newProjectAccessResource() resource.Resource { return &projectAccessResource{} }

type projectAccessModel struct {
	ID        types.String `tfsdk:"id"`
	ProjectID types.Int64  `tfsdk:"project_id"`
	TeamID    types.Int64  `tfsdk:"team_id"`
	Role      types.String `tfsdk:"role"`
}

func (r *projectAccessResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_access"
}

func (r *projectAccessResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Grants a team a role on a project. A team has at most one role per project.\n\n" +
			"Import with `<project_id>/<team_id>`.\n\nRequired API key permissions: " + permList(projectAccessPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "`<project_id>/<team_id>`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the project.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"team_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the team.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"role": schema.StringAttribute{Required: true, MarkdownDescription: "`owner`, `admin`, `deployer` or `viewer`. Changes in place.",
				Validators: []validator.String{stringvalidator.OneOf("owner", "admin", "deployer", "viewer")}},
		},
	}
}

func (m *projectAccessModel) fromAPI(a *client.ProjectAccess) {
	m.ID = types.StringValue(fmt.Sprintf("%d/%d", a.ProjectID, a.TeamID))
	m.ProjectID = types.Int64Value(a.ProjectID)
	m.TeamID = types.Int64Value(a.TeamID)
	m.Role = types.StringValue(a.Role)
}

func (r *projectAccessResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectAccessModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid, tid := plan.ProjectID.ValueInt64(), plan.TeamID.ValueInt64()
	// The API upserts grants, so a second resource for the same pair would
	// silently take over the first one's role. Refuse instead.
	existing, err := r.client.GetProjectAccess(ctx, pid, tid)
	if err == nil {
		resp.Diagnostics.AddError("Team already has access to the project",
			fmt.Sprintf("Team %d already has the %q role on project %d. Import it to manage it with Terraform: terraform import <address> %d/%d",
				tid, existing.Role, pid, pid, tid))
		return
	}
	if !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to read the access grants of project %d", pid), err, projectAccessPermissions)
		return
	}
	a, err := r.client.GrantProjectAccess(ctx, pid, tid, plan.Role.ValueString())
	if err != nil {
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to grant team %d access to project %d", tid, pid), err, projectAccessPermissions)
		return
	}
	plan.fromAPI(a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectAccessResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectAccessModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid, tid := state.ProjectID.ValueInt64(), state.TeamID.ValueInt64()
	a, err := r.client.GetProjectAccess(ctx, pid, tid)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to read team %d's access to project %d", tid, pid), err, projectAccessPermissions)
		return
	}
	state.fromAPI(a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *projectAccessResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan projectAccessModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid, tid := plan.ProjectID.ValueInt64(), plan.TeamID.ValueInt64()
	a, err := r.client.GrantProjectAccess(ctx, pid, tid, plan.Role.ValueString())
	if err != nil {
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to change team %d's role on project %d", tid, pid), err, projectAccessPermissions)
		return
	}
	plan.fromAPI(a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectAccessResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectAccessModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid, tid := state.ProjectID.ValueInt64(), state.TeamID.ValueInt64()
	if err := r.client.RevokeProjectAccess(ctx, pid, tid); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to revoke team %d's access to project %d", tid, pid), err, projectAccessPermissions)
	}
}

func (r *projectAccessResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	pid, tid, err := parseCompositeID(req.ID, "<project_id>/<team_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), pid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("team_id"), tid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
