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
)

const envAddr = "temps_environment.staging"

func environmentSteps(h *harness) []resource.TestStep {
	parent := h.projectConfig("envs")
	return []resource.TestStep{
		{
			Config: h.config(parent + `
resource "temps_environment" "staging" {
  project_id = temps_project.parent.id
  name       = "staging"
  branch     = "develop"
}`),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(envAddr, "name", "staging"),
				resource.TestCheckResourceAttr(envAddr, "branch", "develop"),
				resource.TestCheckResourceAttrSet(envAddr, "main_url"),
				resource.TestCheckResourceAttrPair(envAddr, "project_id", "temps_project.parent", "id"),
				resource.TestCheckResourceAttrSet("data.temps_environment.production", "id"),
			),
		},
		{
			ResourceName: envAddr, ImportState: true, ImportStateVerify: true,
			ImportStateIdFunc: stateID(envAddr, "project_id", "id"),
		},
		{
			Config: h.config(parent + `
resource "temps_environment" "staging" {
  project_id = temps_project.parent.id
  name       = "staging"
  branch     = "release"
}`),
			ConfigPlanChecks: expectAction(envAddr, plancheck.ResourceActionUpdate),
			Check:            resource.TestCheckResourceAttr(envAddr, "branch", "release"),
		},
		{
			Config: h.config(parent + `
resource "temps_environment" "staging" {
  project_id = temps_project.parent.id
  name       = "qa"
  branch     = "release"
}`),
			ConfigPlanChecks: expectAction(envAddr, plancheck.ResourceActionReplace),
			Check:            resource.TestCheckResourceAttr(envAddr, "name", "qa"),
		},
	}
}

func (h *harness) environmentDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_environment", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetEnvironment(context.Background(), mustID(rs.Primary.Attributes["project_id"]), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitEnvironment(t *testing.T) {
	h := unitHarness(t)
	h.run(environmentSteps(h), h.environmentDestroyed())
}

func TestAccEnvironment(t *testing.T) {
	h := accHarness(t)
	h.run(environmentSteps(h), h.environmentDestroyed())
}

func TestUnitEnvironmentDataSourceUnknownName(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: h.config(`
resource "temps_project" "parent" {
  name        = "web"
  source_type = "docker_image"
}

data "temps_environment" "missing" {
  project_id = temps_project.parent.id
  name       = "nope"
}`),
		ExpectError: regexp.MustCompile(`(?s)has no environment named "nope"; it has:\s+"production"`),
	}}, nil)
}

func TestUnitEnvironmentImportBadID(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{
		{Config: h.config(h.projectConfig("x"))},
		{
			Config:       h.config(h.projectConfig("x") + `resource "temps_environment" "staging" {` + "\n" + `project_id = 1` + "\n" + `name = "staging"` + "\n" + `branch = "main"` + "\n}"),
			ResourceName: envAddr, ImportState: true, ImportStateId: "12",
			ExpectError: regexp.MustCompile(`must have the form <project_id>/<environment_id>`),
		},
	}, nil)
}
