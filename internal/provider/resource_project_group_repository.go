// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

// The repository of a project group (PUT/DELETE /project-groups/{id}/repository)
// and whether a member follows it (PUT .../projects/{project}/repository-inheritance).

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

// groupRepositoryPermissions are needed on top of projectGroupPermissions to
// set a repository or make a member follow it.
var groupRepositoryPermissions = []string{"projects:read", "projects:write", "git_connections:read", "git_repositories:read"}

// Temps trims these fields and refuses whitespace inside them.
var gitFieldRE = regexp.MustCompile(`^\S+$`)

type groupRepositoryModel struct {
	RepoOwner               types.String `tfsdk:"repo_owner"`
	RepoName                types.String `tfsdk:"repo_name"`
	MainBranch              types.String `tfsdk:"main_branch"`
	GitProviderConnectionID types.Int64  `tfsdk:"git_provider_connection_id"`
	PublicProvider          types.String `tfsdk:"public_provider"`
}

func (m *groupRepositoryModel) equal(o *groupRepositoryModel) bool {
	if m == nil || o == nil {
		return m == o
	}
	return m.RepoOwner.Equal(o.RepoOwner) && m.RepoName.Equal(o.RepoName) && m.MainBranch.Equal(o.MainBranch) &&
		m.GitProviderConnectionID.Equal(o.GitProviderConnectionID) && m.PublicProvider.Equal(o.PublicProvider)
}

func (m *groupRepositoryModel) request() client.ProjectGroupRepository {
	out := client.ProjectGroupRepository{
		RepoOwner: m.RepoOwner.ValueString(), RepoName: m.RepoName.ValueString(), MainBranch: m.MainBranch.ValueString(),
		PublicProvider: strPtr(m.PublicProvider),
	}
	if known(m.GitProviderConnectionID) {
		out.GitProviderConnectionID = ptr(m.GitProviderConnectionID.ValueInt64())
	}
	return out
}

func groupRepositoryFromAPI(r *client.ProjectGroupRepository) *groupRepositoryModel {
	if r == nil {
		return nil
	}
	return &groupRepositoryModel{
		RepoOwner: types.StringValue(r.RepoOwner), RepoName: types.StringValue(r.RepoName), MainBranch: types.StringValue(r.MainBranch),
		GitProviderConnectionID: types.Int64PointerValue(r.GitProviderConnectionID), PublicProvider: types.StringPointerValue(r.PublicProvider),
	}
}

func groupRepositoryAttribute() schema.Attribute {
	field := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Required: true, MarkdownDescription: desc,
			Validators: []validator.String{stringvalidator.RegexMatches(gitFieldRE, "must not be empty or contain spaces")}}
	}
	return schema.SingleNestedAttribute{
		Optional: true,
		MarkdownDescription: "Repository the group's projects can follow, for monorepos (see `inherit_repository` on " +
			"`temps_project_group_membership`). Setting or changing it rewrites the repository and branch of every member " +
			"that follows it, keeping each member's directory; a member that cannot be updated keeps its previous settings " +
			"and is reported as a warning. Left out, Terraform does not manage the repository; removing the argument removes " +
			"the repository from the group, and its members keep their current git settings and stop following. An import " +
			"does not read the repository: add the argument afterwards to manage it (if it matches Temps, nothing is " +
			"rewritten). Read back " +
			"from `GET /project-groups/{id}` while set. Needs " + permList(groupRepositoryPermissions) + " and write access " +
			"to every project in the group.",
		Attributes: map[string]schema.Attribute{
			"repo_owner":  field("Repository owner or organization."),
			"repo_name":   field("Repository name."),
			"main_branch": field("Branch the following projects deploy from; it must exist."),
			"git_provider_connection_id": schema.Int64Attribute{Optional: true,
				MarkdownDescription: "Git provider connection to read the repository through; the repository must be in its " +
					"synced list. Set exactly one of this and `public_provider`.",
				Validators: []validator.Int64{int64validator.AtLeast(1),
					int64validator.ExactlyOneOf(path.MatchRelative().AtParent().AtName("public_provider"))}},
			"public_provider": schema.StringAttribute{Optional: true,
				MarkdownDescription: "`github` or `gitlab`, to follow a public repository without a connection. Pushes to it " +
					"cannot trigger deploys.",
				Validators: []validator.String{stringvalidator.OneOf("github", "gitlab")}},
		},
	}
}

// writeGroupRepository brings the group's repository from prior to plan:
// PUT when it is configured and differs, DELETE when it was managed and is
// no longer configured. It returns the updated group, or nil if nothing was
// written.
func (r *projectGroupResource) writeGroupRepository(ctx context.Context, id int64, plan, prior *groupRepositoryModel, diags *diag.Diagnostics) *client.ProjectGroup {
	switch {
	case plan.equal(prior):
		return nil
	case plan == nil:
		g, err := r.client.ClearProjectGroupRepository(ctx, id)
		if err != nil {
			addAPIError(diags, fmt.Sprintf("Unable to remove the repository of project group %d", id), err, groupRepositoryPermissions)
			return nil
		}
		return g
	}
	g, services, err := r.client.SetProjectGroupRepository(ctx, id, plan.request())
	if err != nil {
		addAPIError(diags, fmt.Sprintf("Unable to set the repository of project group %d", id), err, groupRepositoryPermissions)
		return nil
	}
	var failed []string
	for _, s := range services {
		if s.Error != nil {
			failed = append(failed, fmt.Sprintf("- %s (%d): %s", s.Name, s.ProjectID, *s.Error))
		}
	}
	if len(failed) > 0 {
		diags.AddWarning("Some projects still use the previous repository",
			fmt.Sprintf("Project group %d now uses %s/%s@%s, but these projects that follow it could not be updated and keep "+
				"their previous repository or branch:\n%s\n\nFix them and apply again, or set the repository from the console.",
				id, plan.RepoOwner.ValueString(), plan.RepoName.ValueString(), plan.MainBranch.ValueString(), strings.Join(failed, "\n")))
	}
	return g
}

// inheritRepositoryAttribute is temps_project_group_membership's switch.
func inheritRepositoryAttribute() schema.Attribute {
	return schema.BoolAttribute{Optional: true, Computed: true,
		MarkdownDescription: "Whether the project follows the group's `repository`: `true` points the project at the group's " +
			"repository and branch now (keeping its own directory) and on every later change; `false` keeps its current git " +
			"settings and stops following. The group must have a repository to follow, and the project must deploy from Git. " +
			"Left out, Terraform leaves it as it is. Removing the group's repository sets it to `false` on every member. " +
			"Leave `repo_owner`, `repo_name`, `main_branch` and `git_provider_connection_id` out of the project's own " +
			"`temps_project` while it follows, as the group sets them. Read back from `GET /project-groups/{id}`. Needs " +
			permList(groupRepositoryPermissions) + " to change.",
		PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}}
}
