// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

const (
	connectedRepo = `repository = {
    repo_owner                 = "example-org"
    repo_name                  = "shop"
    main_branch                = "main"
    git_provider_connection_id = 7
  }`
	publicRepo = `repository = {
    repo_owner      = "example-org"
    repo_name       = "shop-public"
    main_branch     = "release"
    public_provider = "github"
  }`
)

func groupRepoConfig(h *harness, repo, inherit string) string {
	return h.config(h.projectConfig("group")+`
resource "temps_project_group" "web" {
  name = "Web apps"
  slug = %q
  %s
}

resource "temps_project_group_membership" "web" {
  group_id   = temps_project_group.web.id
  project_id = temps_project.parent.id
  %s
}`, h.name("web"), repo, inherit)
}

// onlyGroup runs fn on the fake's only project group.
func (h *harness) onlyGroup(fn func(g *client.ProjectGroup)) {
	h.fake.with(func() {
		for _, g := range h.fake.x.groups {
			fn(g)
		}
	})
}

func (h *harness) groupState(check func(g *client.ProjectGroup) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		var err error
		h.onlyGroup(func(g *client.ProjectGroup) { err = check(g) })
		return err
	}
}

func wantFollowing(want bool) func(g *client.ProjectGroup) error {
	return func(g *client.ProjectGroup) error {
		if len(g.Projects) != 1 || g.Projects[0].InheritsRepository == nil || *g.Projects[0].InheritsRepository != want {
			return fmt.Errorf("members are %+v, want one with inherits_repository = %t", g.Projects, want)
		}
		return nil
	}
}

func (h *harness) groupWrites() (repo, inherit int) {
	h.fake.with(func() { repo, inherit = h.fake.st.repoSets, h.fake.st.inheritSets })
	return
}

func TestUnitProjectGroupRepository(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{
		{
			Config:           groupRepoConfig(h, connectedRepo, "inherit_repository = true"),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(groupAddr, "repository.repo_owner", "example-org"),
				resource.TestCheckResourceAttr(groupAddr, "repository.git_provider_connection_id", "7"),
				resource.TestCheckNoResourceAttr(groupAddr, "repository.public_provider"),
				resource.TestCheckResourceAttr(memberAddr, "inherit_repository", "true"),
				h.groupState(func(g *client.ProjectGroup) error {
					if g.Repository == nil || g.Repository.RepoName != "shop" || g.Repository.MainBranch != "main" {
						return fmt.Errorf("group repository is %+v", g.Repository)
					}
					return wantFollowing(true)(g)
				}),
			),
		},
		{ResourceName: groupAddr, ImportState: true, ImportStateVerify: true},
		{ResourceName: memberAddr, ImportState: true, ImportStateVerify: true},
		{
			Config:           groupRepoConfig(h, publicRepo, "inherit_repository = true"),
			ConfigPlanChecks: expectAction(groupAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(groupAddr, "repository.public_provider", "github"),
				resource.TestCheckNoResourceAttr(groupAddr, "repository.git_provider_connection_id"),
				resource.TestCheckResourceAttr(groupAddr, "repository.main_branch", "release"),
				h.groupState(func(g *client.ProjectGroup) error {
					if g.Repository.GitProviderConnectionID != nil || *g.Repository.PublicProvider != "github" {
						return fmt.Errorf("group repository is %+v", g.Repository)
					}
					return nil
				}),
			),
		},
		{
			// A branch changed in the console is set back; a member that
			// stopped following follows again.
			PreConfig: func() {
				h.onlyGroup(func(g *client.ProjectGroup) {
					g.Repository.MainBranch = "hotfix"
					g.Projects[0].InheritsRepository = ptr(false)
				})
			},
			Config: groupRepoConfig(h, publicRepo, "inherit_repository = true"),
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction(groupAddr, plancheck.ResourceActionUpdate),
				plancheck.ExpectResourceAction(memberAddr, plancheck.ResourceActionUpdate),
			}},
			Check: h.groupState(func(g *client.ProjectGroup) error {
				if g.Repository.MainBranch != "release" {
					return fmt.Errorf("branch is %q", g.Repository.MainBranch)
				}
				return wantFollowing(true)(g)
			}),
		},
		{
			Config:           groupRepoConfig(h, publicRepo, "inherit_repository = false"),
			ConfigPlanChecks: expectAction(memberAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(memberAddr, "inherit_repository", "false"),
				h.groupState(wantFollowing(false)),
			),
		},
		{
			// Left out, inherit_repository is not managed: a console change
			// plans nothing.
			PreConfig:        func() { h.onlyGroup(func(g *client.ProjectGroup) { g.Projects[0].InheritsRepository = ptr(true) }) },
			Config:           groupRepoConfig(h, publicRepo, ""),
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(memberAddr, "inherit_repository", "true"),
				h.groupState(wantFollowing(true)),
			),
		},
		{
			// Removing the repository removes it from the group; members
			// stop following.
			Config:           groupRepoConfig(h, "", ""),
			ConfigPlanChecks: expectAction(groupAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr(groupAddr, "repository.repo_owner"),
				h.groupState(func(g *client.ProjectGroup) error {
					if g.Repository != nil {
						return fmt.Errorf("group still has repository %+v", g.Repository)
					}
					return wantFollowing(false)(g)
				}),
			),
		},
		{
			// A repository set in the console is not managed while left out.
			PreConfig: func() {
				h.onlyGroup(func(g *client.ProjectGroup) {
					g.Repository = &client.ProjectGroupRepository{RepoOwner: "example-org", RepoName: "other", MainBranch: "main", PublicProvider: ptr("gitlab")}
				})
			},
			Config:           groupRepoConfig(h, "", ""),
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			Check:            resource.TestCheckNoResourceAttr(groupAddr, "repository.repo_owner"),
		},
		{
			// Importing picks it up.
			ResourceName: groupAddr, ImportState: true, ImportStateCheck: func(s []*terraform.InstanceState) error {
				if got := s[0].Attributes["repository.repo_name"]; got != "other" {
					return fmt.Errorf("imported repository.repo_name = %q", got)
				}
				return nil
			},
		},
	}, nil)
}

// Configurations from before the upgrade (no repository, no
// inherit_repository) plan nothing and write nothing.
func TestUnitProjectGroupRepositoryUnsetLeavesServerAlone(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{
		{
			Config:           groupRepoConfig(h, "", ""),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check:            resource.TestCheckResourceAttr(memberAddr, "inherit_repository", "false"),
		},
		{
			PreConfig: func() {
				h.onlyGroup(func(g *client.ProjectGroup) {
					g.Repository = &client.ProjectGroupRepository{RepoOwner: "example-org", RepoName: "shop", MainBranch: "main", GitProviderConnectionID: ptr(int64(7))}
					g.Projects[0].InheritsRepository = ptr(true)
				})
			},
			Config:           groupRepoConfig(h, "", ""),
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			Check: func(*terraform.State) error {
				if repo, inherit := h.groupWrites(); repo+inherit != 0 {
					return fmt.Errorf("%d repository and %d inheritance writes, want none", repo, inherit)
				}
				return nil
			},
		},
	}, nil)
}

func TestUnitProjectGroupRepositoryErrors(t *testing.T) {
	t.Run("validation", func(t *testing.T) {
		h := unitHarness(t)
		for _, tc := range []struct{ repo, want string }{
			{`repository = { repo_owner = "o", repo_name = "n", main_branch = "main" }`, `No attribute specified when one \(and only one\)`},
			{`repository = { repo_owner = "o", repo_name = "n", main_branch = "main", git_provider_connection_id = 7, public_provider = "github" }`, `2 attributes specified when one \(and only one\)`},
			{`repository = { repo_owner = "o", repo_name = "n", main_branch = "main", public_provider = "bitbucket" }`, `value must be one of`},
			{`repository = { repo_owner = "my org", repo_name = "n", main_branch = "main", public_provider = "github" }`, `must not be empty or contain spaces`},
			{`repository = { repo_owner = "o", repo_name = "n", main_branch = "", public_provider = "github" }`, `must not be empty or contain spaces`},
		} {
			h.run([]resource.TestStep{{Config: groupRepoConfig(h, tc.repo, ""), PlanOnly: true, ExpectError: regexp.MustCompile(tc.want)}}, nil)
		}
	})
	t.Run("refused by the server", func(t *testing.T) {
		h := unitHarness(t)
		bad := fmt.Sprintf(`repository = { repo_owner = "o", repo_name = "n", main_branch = "main", git_provider_connection_id = %d }`, badConnection)
		h.run([]resource.TestStep{
			{Config: groupRepoConfig(h, "", "")},
			{
				Config:      groupRepoConfig(h, bad, ""),
				ExpectError: regexp.MustCompile(`(?s)Unable to set the repository of project group.*it does not exist`),
			},
			{
				// The group kept no repository, so nothing was recorded.
				Config:           groupRepoConfig(h, "", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{
				Config:      groupRepoConfig(h, "", "inherit_repository = true"),
				ExpectError: regexp.MustCompile(`(?s)Unable to make project \d+ follow the repository.*409.*has no repository\s+yet`),
			},
		}, nil)
	})
	t.Run("followers that cannot be updated only warn", func(t *testing.T) {
		h := unitHarness(t)
		h.run([]resource.TestStep{
			{Config: groupRepoConfig(h, connectedRepo, "inherit_repository = true")},
			{
				PreConfig:        func() { h.fake.with(func() { h.fake.st.syncFails = true }) },
				Config:           groupRepoConfig(h, publicRepo, "inherit_repository = true"),
				ConfigPlanChecks: emptyPlanAfterApply(),
				Check:            resource.TestCheckResourceAttr(groupAddr, "repository.public_provider", "github"),
			},
		}, nil)
	})
	t.Run("old server", func(t *testing.T) {
		h := unitHarness(t)
		h.fake.st.old = true
		h.run([]resource.TestStep{
			{
				Config:           groupRepoConfig(h, "", ""),
				ConfigPlanChecks: emptyPlanAfterApply(),
				Check:            resource.TestCheckNoResourceAttr(memberAddr, "inherit_repository"),
			},
			{ResourceName: memberAddr, ImportState: true, ImportStateVerify: true},
			{
				Config:      groupRepoConfig(h, connectedRepo, ""),
				ExpectError: regexp.MustCompile(`(?s)Unable to set the repository.*does not provide this endpoint`),
			},
		}, nil)
	})
}

// A membership created with inherit_repository on an old server fails with
// a clear message instead of silently doing nothing.
func TestUnitProjectGroupInheritOldServer(t *testing.T) {
	h := unitHarness(t)
	h.fake.st.old = true
	h.run([]resource.TestStep{{
		Config:      groupRepoConfig(h, "", "inherit_repository = false"),
		ExpectError: regexp.MustCompile(`(?s)Unable to set inherit_repository.*older than group repositories`),
	}}, nil)
}
