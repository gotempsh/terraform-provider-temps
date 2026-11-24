// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

const (
	groupAddr       = "temps_project_group.web"
	memberAddr      = "temps_project_group_membership.web"
	groupVarAddr    = "temps_project_group_variable.region"
	groupSecretAddr = "temps_project_group_variable.token"
	groupDataAddr   = "data.temps_project_group.web"
)

func projectGroupConfig(h *harness, slug, description, region string) string {
	return h.config(h.projectConfig("group")+`
resource "temps_project_group" "web" {
  name = "Web apps"
  slug = %q
  %s
}

resource "temps_project_group_membership" "web" {
  group_id   = temps_project_group.web.id
  project_id = temps_project.parent.id
}

resource "temps_project_group_variable" "region" {
  group_id           = temps_project_group.web.id
  key                = "REGION"
  value              = %q
  include_in_preview = true
}

resource "temps_project_group_variable" "token" {
  group_id  = temps_project_group.web.id
  key       = "SHARED_TOKEN"
  value     = "not-a-real-token"
  is_secret = true
}

data "temps_project_group" "web" {
  slug       = temps_project_group.web.slug
  depends_on = [temps_project_group_membership.web]
}`, slug, description, region)
}

func projectGroupSteps(h *harness) []resource.TestStep {
	slug := h.name("web")
	return []resource.TestStep{
		{
			Config:           projectGroupConfig(h, slug, `description = "Front ends"`, "eu"),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(groupAddr, "description", "Front ends"),
				resource.TestCheckResourceAttr(groupVarAddr, "value", "eu"),
				resource.TestCheckResourceAttr(groupSecretAddr, "is_secret", "true"),
				resource.TestCheckResourceAttr(groupDataAddr, "name", "Web apps"),
				resource.TestCheckResourceAttr(groupDataAddr, "project_ids.#", "1"),
				resource.TestCheckResourceAttrPair(groupDataAddr, "id", groupAddr, "id"),
			),
		},
		{ResourceName: groupAddr, ImportState: true, ImportStateVerify: true},
		{ResourceName: memberAddr, ImportState: true, ImportStateVerify: true},
		// Temps lists every value masked (*** or null), so an import cannot
		// know a value.
		{ResourceName: groupVarAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"value"}},
		{ResourceName: groupSecretAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"value"}},
		{
			Config: projectGroupConfig(h, slug, "", "us"),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(groupAddr, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(groupVarAddr, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(memberAddr, plancheck.ResourceActionNoop),
				},
				PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
			},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr(groupAddr, "description"),
				resource.TestCheckResourceAttr(groupVarAddr, "value", "us"),
			),
		},
	}
}

func (h *harness) projectGroupsDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_project_group", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetProjectGroup(context.Background(), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitProjectGroup(t *testing.T) {
	h := unitHarness(t)
	h.run(projectGroupSteps(h), h.projectGroupsDestroyed())
}

func TestAccProjectGroup(t *testing.T) {
	h := accHarness(t)
	h.run(projectGroupSteps(h), h.projectGroupsDestroyed())
}

// Temps moves a project between groups silently, so the provider refuses to
// take over a project another group already holds instead of stealing it.
func TestUnitProjectGroupMembershipRefusesGroupedProject(t *testing.T) {
	h := unitHarness(t)
	cfg := h.config(h.projectConfig("group") + `
resource "temps_project_group" "web" {
  name = "Web apps"
  slug = "web"
}`)
	h.run([]resource.TestStep{
		{Config: cfg},
		{
			PreConfig: func() {
				h.fake.with(func() {
					other := &client.ProjectGroup{ID: h.fake.nextID(), Name: "Other", Slug: "other"}
					for pid, p := range h.fake.projects {
						other.Projects = append(other.Projects, client.ProjectGroupMember{ID: pid, Name: p.Name, Slug: p.Slug})
					}
					h.fake.x.groups[other.ID] = other
				})
			},
			Config: cfg + `
resource "temps_project_group_membership" "web" {
  group_id   = temps_project_group.web.id
  project_id = temps_project.parent.id
}`,
			ExpectError: regexp.MustCompile(`already in a project group`),
		},
	}, nil)
}

func TestUnitProjectGroupDataSourceNeedsOneSelector(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config:      h.config(`data "temps_project_group" "x" {}`),
		ExpectError: regexp.MustCompile(`(?i)exactly one|invalid attribute combination`),
	}}, nil)
}
