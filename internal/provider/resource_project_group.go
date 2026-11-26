// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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
	projectGroupPermissions           = []string{"projects:read", "projects:create", "projects:write"}
	projectGroupMembershipPermissions = []string{"projects:read", "projects:write"}
	projectGroupVariablePermissions   = []string{"projects:read", "environments:read", "environments:write"}
)

var (
	_ resource.Resource                = &projectGroupResource{}
	_ resource.ResourceWithImportState = &projectGroupResource{}
	_ resource.Resource                = &projectGroupMembershipResource{}
	_ resource.ResourceWithImportState = &projectGroupMembershipResource{}
	_ resource.Resource                = &projectGroupVariableResource{}
	_ resource.ResourceWithImportState = &projectGroupVariableResource{}
)

// ------------------------------------------------------------ temps_project_group

type projectGroupResource struct{ resourceBase }

func newProjectGroupResource() resource.Resource { return &projectGroupResource{} }

type projectGroupModel struct {
	ID          types.String          `tfsdk:"id"`
	Name        types.String          `tfsdk:"name"`
	Slug        types.String          `tfsdk:"slug"`
	Description types.String          `tfsdk:"description"`
	Repository  *groupRepositoryModel `tfsdk:"repository"`
}

func (r *projectGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_group"
}

func (r *projectGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A group of projects that share environment variables (`temps_project_group_variable`). " +
			"Add projects with `temps_project_group_membership`. A group can also hold the repository its projects follow " +
			"(monorepos). Deleting a group keeps its projects.\n\n" +
			"Import with the group ID.\n\nRequired API key permissions: " + permList(projectGroupPermissions) +
			"; `repository` also needs " + permList(groupRepositoryPermissions[2:]) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric group ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Display name.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"slug": schema.StringAttribute{Optional: true, Computed: true,
				MarkdownDescription: "Unique identifier: lower-case letters, digits and `-`. Derived from the name when omitted; changes in place.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:          []validator.String{stringvalidator.RegexMatches(teamSlugRE, "must contain only lower-case letters, digits and '-'")}},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "What the group is for.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"repository": groupRepositoryAttribute(),
		},
	}
}

// fromAPI copies the group. The repository is read only while managed: an
// import leaves it out, so state holds a repository only once the
// configuration has set it, and removing the argument then removes it.
func (m *projectGroupModel) fromAPI(g *client.ProjectGroup) {
	m.ID = types.StringValue(formatID(g.ID))
	m.Name = types.StringValue(g.Name)
	m.Slug = types.StringValue(g.Slug)
	if g.Description == nil || *g.Description == "" {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue(*g.Description)
	}
	if m.Repository != nil {
		m.Repository = groupRepositoryFromAPI(g.Repository)
	}
}

func (r *projectGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.client.CreateProjectGroup(ctx, client.ProjectGroupRequest{Name: strPtr(plan.Name), Slug: strPtr(plan.Slug), Description: strPtr(plan.Description)})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create project group "+plan.Name.String(), err, projectGroupPermissions)
		return
	}
	if plan.Repository != nil {
		if updated := r.writeGroupRepository(ctx, g.ID, plan.Repository, nil, &resp.Diagnostics); updated != nil {
			g = updated
		} else {
			// The group exists without its repository: keep it in state so
			// it is not orphaned (Terraform marks it tainted).
			plan.Repository = nil
		}
	}
	plan.fromAPI(g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_project_group ID", err.Error())
		return
	}
	g, err := r.client.GetProjectGroup(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read project group "+formatID(id), err, projectGroupPermissions)
		return
	}
	state.fromAPI(g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *projectGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state projectGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_project_group ID", err.Error())
		return
	}
	g, err := r.client.UpdateProjectGroup(ctx, id, client.ProjectGroupRequest{
		Name: strPtr(plan.Name), Slug: strPtr(plan.Slug), Description: clearable(plan.Description, state.Description),
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update project group "+formatID(id), err, projectGroupPermissions)
		return
	}
	prior := state.Repository
	if plan.Repository != nil && prior == nil {
		// Configured for the first time (for example after an import):
		// compare with what Temps has, so a matching repository is not
		// rewritten onto every follower.
		prior = groupRepositoryFromAPI(g.Repository)
	}
	if updated := r.writeGroupRepository(ctx, id, plan.Repository, prior, &resp.Diagnostics); updated != nil {
		g = updated
	}
	if resp.Diagnostics.HasError() {
		return
	}
	plan.fromAPI(g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_project_group ID", err.Error())
		return
	}
	if err := r.client.DeleteProjectGroup(ctx, id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete project group "+formatID(id), err, projectGroupPermissions)
	}
}

func (r *projectGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := parseID(req.ID); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ------------------------------------------------------------ temps_project_group_membership

type projectGroupMembershipResource struct{ resourceBase }

func newProjectGroupMembershipResource() resource.Resource { return &projectGroupMembershipResource{} }

type projectGroupMembershipModel struct {
	ID                types.String `tfsdk:"id"`
	GroupID           types.Int64  `tfsdk:"group_id"`
	ProjectID         types.Int64  `tfsdk:"project_id"`
	InheritRepository types.Bool   `tfsdk:"inherit_repository"`
}

// fromGroup copies the membership from the group's member entry; a server
// without group repositories leaves inherit_repository as it is (null when
// unknown).
func (m *projectGroupMembershipModel) fromGroup(member *client.ProjectGroupMember) {
	m.ID = types.StringValue(fmt.Sprintf("%d/%d", m.GroupID.ValueInt64(), m.ProjectID.ValueInt64()))
	switch {
	case member != nil && member.InheritsRepository != nil:
		m.InheritRepository = types.BoolValue(*member.InheritsRepository)
	case m.InheritRepository.IsUnknown():
		m.InheritRepository = types.BoolNull()
	}
}

// setInheritance makes the project follow (or stop following) the group's
// repository when the plan asks for something other than current.
func (r *projectGroupMembershipResource) setInheritance(ctx context.Context, plan *projectGroupMembershipModel, current types.Bool, diags *diag.Diagnostics) *client.ProjectGroupMember {
	if !known(plan.InheritRepository) || plan.InheritRepository.Equal(current) {
		return nil
	}
	gid, pid := plan.GroupID.ValueInt64(), plan.ProjectID.ValueInt64()
	g, err := r.client.SetRepositoryInheritance(ctx, gid, pid, plan.InheritRepository.ValueBool())
	if err != nil {
		verb := "stop project %d following"
		if plan.InheritRepository.ValueBool() {
			verb = "make project %d follow"
		}
		addAPIError(diags, fmt.Sprintf("Unable to "+verb+" the repository of project group %d", pid, gid), err, groupRepositoryPermissions)
		return nil
	}
	return g.Member(pid)
}

func (r *projectGroupMembershipResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_group_membership"
}

func (r *projectGroupMembershipResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Puts a project in a project group, optionally following the group's repository. A project " +
			"belongs to at most one group; creating this resource for a project that is already in a group fails instead of moving it.\n\n" +
			"Import with `<group_id>/<project_id>`.\n\nRequired API key permissions: " + permList(projectGroupMembershipPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "`<group_id>/<project_id>`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"group_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the project group.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"project_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the project.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"inherit_repository": inheritRepositoryAttribute(),
		},
	}
}

func (r *projectGroupMembershipResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectGroupMembershipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	gid, pid := plan.GroupID.ValueInt64(), plan.ProjectID.ValueInt64()
	// The API moves a project out of its current group without saying so,
	// which would silently take it from another configuration. Refuse.
	current, err := r.client.FindProjectGroupOf(ctx, pid)
	if err != nil {
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to find the group of project %d", pid), err, projectGroupMembershipPermissions)
		return
	}
	if current != nil {
		resp.Diagnostics.AddError("Project is already in a project group",
			fmt.Sprintf("Project %d is in project group %d (%q). Remove it from that group first, or, if it is group %d, import it: terraform import <address> %d/%d",
				pid, current.ID, current.Slug, gid, current.ID, pid))
		return
	}
	if err := r.client.AddProjectToGroup(ctx, gid, pid); err != nil {
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to add project %d to project group %d", pid, gid), err, projectGroupMembershipPermissions)
		return
	}
	g, err := r.client.GetProjectGroup(ctx, gid)
	if err != nil {
		// The project is in the group now: keep the membership in state
		// (Terraform marks it tainted) so it is not orphaned. An
		// inherit_repository that was not written is left null, so the next
		// apply sets it.
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Project %d was added to project group %d, but reading the group back failed", pid, gid), err, projectGroupMembershipPermissions)
		plan.InheritRepository = types.BoolUnknown()
		plan.fromGroup(nil)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	member := g.Member(pid)
	configured := plan.InheritRepository
	if member != nil && member.InheritsRepository != nil {
		// The project is in the group now; a failure below leaves it there
		// with Temps' value, and Terraform marks the membership tainted.
		if m := r.setInheritance(ctx, &plan, types.BoolValue(*member.InheritsRepository), &resp.Diagnostics); m != nil {
			member = m
		}
	} else if known(configured) {
		resp.Diagnostics.AddError("Unable to set inherit_repository",
			fmt.Sprintf("Temps did not report whether project %d follows the repository of project group %d. The server is "+
				"probably older than group repositories; upgrade Temps or remove the argument.", pid, gid))
	}
	plan.InheritRepository = types.BoolUnknown()
	plan.fromGroup(member)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectGroupMembershipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectGroupMembershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	gid, pid := state.GroupID.ValueInt64(), state.ProjectID.ValueInt64()
	g, err := r.client.GetProjectGroup(ctx, gid)
	if client.IsNotFound(err) || (err == nil && !g.HasProject(pid)) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to read project group %d", gid), err, projectGroupMembershipPermissions)
		return
	}
	state.fromGroup(g.Member(pid))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update only changes inherit_repository; the other attributes replace.
func (r *projectGroupMembershipResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state projectGroupMembershipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	member := r.setInheritance(ctx, &plan, state.InheritRepository, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if member == nil {
		plan.InheritRepository = state.InheritRepository
	} else {
		plan.fromGroup(member)
	}
	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectGroupMembershipResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectGroupMembershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	gid, pid := state.GroupID.ValueInt64(), state.ProjectID.ValueInt64()
	if err := r.client.RemoveProjectFromGroup(ctx, gid, pid); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to remove project %d from project group %d", pid, gid), err, projectGroupMembershipPermissions)
	}
}

func (r *projectGroupMembershipResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	gid, pid, err := parseCompositeID(req.ID, "<group_id>/<project_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), gid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), pid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// ------------------------------------------------------------ temps_project_group_variable

type projectGroupVariableResource struct{ resourceBase }

func newProjectGroupVariableResource() resource.Resource { return &projectGroupVariableResource{} }

type projectGroupVariableModel struct {
	ID               types.String `tfsdk:"id"`
	GroupID          types.Int64  `tfsdk:"group_id"`
	Key              types.String `tfsdk:"key"`
	Value            types.String `tfsdk:"value"`
	EnvironmentSlug  types.String `tfsdk:"environment_slug"`
	IncludeInPreview types.Bool   `tfsdk:"include_in_preview"`
	IsSecret         types.Bool   `tfsdk:"is_secret"`
}

func (r *projectGroupVariableResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_group_variable"
}

func (r *projectGroupVariableResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An environment variable every project in a project group receives. " +
			"The API returns a value only when it is written (lists show `***`, and never a secret's value), " +
			"so a value changed outside Terraform is not detected.\n\n" +
			"Import with `<group_id>/<variable_id>`; set `value` in configuration afterwards.\n\n" +
			"Required API key permissions: " + permList(projectGroupVariablePermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "`<group_id>/<variable_id>`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"group_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the project group.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"key": schema.StringAttribute{Required: true, MarkdownDescription: "Variable name. Changing it replaces the variable.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.RegexMatches(envKeyPattern, "must start with a letter or '_' and contain only letters, digits and '_'")}},
			"value": schema.StringAttribute{Required: true, Sensitive: true, MarkdownDescription: "Variable value."},
			"environment_slug": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Only give the variable to this environment of each project (for example `production`); every environment when omitted. Changing it replaces the variable.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)}},
			"include_in_preview": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Also give the variable to preview environments. Defaults to `false`."},
			"is_secret": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Hide the value in the API and console. Defaults to `false`."},
		},
	}
}

// maskedGroupValue is what the API lists in place of a non-secret value.
const maskedGroupValue = "***"

// fromAPI keeps the configured value when the API withholds it: lists mask
// every value and secrets are always null.
func (m *projectGroupVariableModel) fromAPI(groupID int64, v *client.ProjectGroupVariable) {
	m.ID = types.StringValue(fmt.Sprintf("%d/%d", groupID, v.ID))
	m.GroupID = types.Int64Value(groupID)
	m.Key = types.StringValue(v.Key)
	if v.Value != nil && *v.Value != maskedGroupValue {
		m.Value = types.StringValue(*v.Value)
	}
	if v.EnvironmentSlug == nil || *v.EnvironmentSlug == "" {
		m.EnvironmentSlug = types.StringNull()
	} else {
		m.EnvironmentSlug = types.StringValue(*v.EnvironmentSlug)
	}
	m.IncludeInPreview = types.BoolValue(v.IncludeInPreview)
	m.IsSecret = types.BoolValue(v.IsSecret)
}

func (m *projectGroupVariableModel) ids() (int64, int64, error) {
	return parseCompositeID(m.ID.ValueString(), "<group_id>/<variable_id>")
}

func (r *projectGroupVariableResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectGroupVariableModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	gid := plan.GroupID.ValueInt64()
	v, err := r.client.CreateProjectGroupVariable(ctx, gid, client.CreateProjectGroupVariableRequest{
		Key: plan.Key.ValueString(), Value: plan.Value.ValueString(), EnvironmentSlug: strPtr(plan.EnvironmentSlug),
		IncludeInPreview: plan.IncludeInPreview.ValueBool(), IsSecret: plan.IsSecret.ValueBool(),
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to create variable %s in project group %d", plan.Key.ValueString(), gid), err, projectGroupVariablePermissions)
		return
	}
	plan.fromAPI(gid, v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectGroupVariableResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectGroupVariableModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	gid, vid, err := state.ids()
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_project_group_variable ID", err.Error())
		return
	}
	v, err := r.client.GetProjectGroupVariable(ctx, gid, vid)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to read variable %d of project group %d", vid, gid), err, projectGroupVariablePermissions)
		return
	}
	state.fromAPI(gid, v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *projectGroupVariableResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state projectGroupVariableModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	gid, vid, err := state.ids()
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_project_group_variable ID", err.Error())
		return
	}
	// The API refuses to turn a secret into a plain variable without a
	// value, so the value is always sent.
	v, err := r.client.UpdateProjectGroupVariable(ctx, gid, vid, client.UpdateProjectGroupVariableRequest{
		Value: strPtr(plan.Value), IncludeInPreview: boolPtr(plan.IncludeInPreview), IsSecret: boolPtr(plan.IsSecret),
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to update variable %d of project group %d", vid, gid), err, projectGroupVariablePermissions)
		return
	}
	plan.ID = state.ID
	plan.fromAPI(gid, v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectGroupVariableResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectGroupVariableModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	gid, vid, err := state.ids()
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_project_group_variable ID", err.Error())
		return
	}
	if err := r.client.DeleteProjectGroupVariable(ctx, gid, vid); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to delete variable %d of project group %d", vid, gid), err, projectGroupVariablePermissions)
	}
}

func (r *projectGroupVariableResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	gid, _, err := parseCompositeID(req.ID, "<group_id>/<variable_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), gid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
