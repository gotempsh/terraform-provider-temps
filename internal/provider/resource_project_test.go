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

const projectAddr = "temps_project.web"

func projectSteps(h *harness) []resource.TestStep {
	name := h.name("web")
	return []resource.TestStep{
		{ // create
			Config: h.config(`
resource "temps_project" "web" {
  name          = %q
  source_type   = "docker_image"
  preset        = "vite"
  build_command = "npm run build"
  output_dir    = "dist"
  exposed_port  = 3000
}`, name),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(projectAddr, "name", name),
				resource.TestCheckResourceAttrSet(projectAddr, "id"),
				resource.TestCheckResourceAttrSet(projectAddr, "slug"),
				resource.TestCheckResourceAttr(projectAddr, "source_type", "docker_image"),
				resource.TestCheckResourceAttr(projectAddr, "build_command", "npm run build"),
				resource.TestCheckResourceAttr(projectAddr, "output_dir", "dist"),
				resource.TestCheckResourceAttr(projectAddr, "exposed_port", "3000"),
				resource.TestCheckResourceAttr(projectAddr, "automatic_deploy", "false"),
			),
		},
		{ // import by ID
			ResourceName: projectAddr, ImportState: true, ImportStateVerify: true,
		},
		{ // import by slug
			ResourceName: projectAddr, ImportState: true, ImportStateVerify: true,
			ImportStateIdFunc: stateID(projectAddr, "slug"),
		},
		{ // update in place
			Config: h.config(`
resource "temps_project" "web" {
  name                        = "%s-renamed"
  source_type                 = "docker_image"
  preset                      = "vite"
  build_command               = "npm run build:prod"
  install_command             = "npm ci"
  output_dir                  = "dist"
  exposed_port                = 8080
  automatic_deploy            = true
  enable_preview_environments = true
  main_branch                 = "develop"
}`, name),
			ConfigPlanChecks: expectAction(projectAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(projectAddr, "name", name+"-renamed"),
				resource.TestCheckResourceAttr(projectAddr, "build_command", "npm run build:prod"),
				resource.TestCheckResourceAttr(projectAddr, "install_command", "npm ci"),
				resource.TestCheckResourceAttr(projectAddr, "exposed_port", "8080"),
				resource.TestCheckResourceAttr(projectAddr, "automatic_deploy", "true"),
				resource.TestCheckResourceAttr(projectAddr, "enable_preview_environments", "true"),
				resource.TestCheckResourceAttr(projectAddr, "main_branch", "develop"),
			),
		},
		{ // replace: git_url is fixed at creation
			Config: h.config(`
resource "temps_project" "web" {
  name        = "%s-git"
  source_type = "git"
  git_url     = "https://git.example.com/acme/web.git"
  repo_owner  = "acme"
  repo_name   = "web"
  main_branch = "main"
  preset      = "dockerfile"
}`, name),
			ConfigPlanChecks: expectAction(projectAddr, plancheck.ResourceActionReplace),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(projectAddr, "source_type", "git"),
				resource.TestCheckResourceAttr(projectAddr, "repo_owner", "acme"),
				resource.TestCheckResourceAttr(projectAddr, "git_url", "https://git.example.com/acme/web.git"),
			),
		},
	}
}

func (h *harness) projectDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_project", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetProject(context.Background(), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitProject(t *testing.T) {
	h := unitHarness(t)
	h.run(projectSteps(h), h.projectDestroyed())
}

func TestAccProject(t *testing.T) {
	h := accHarness(t)
	h.run(projectSteps(h), h.projectDestroyed())
}

func TestUnitProjectRemovedOutsideTerraform(t *testing.T) {
	h := unitHarness(t)
	cfg := h.config(`
resource "temps_project" "web" {
  name        = "web"
  source_type = "docker_image"
}`)
	h.run([]resource.TestStep{
		{Config: cfg},
		{
			// Deleted outside Terraform: the 404 on refresh removes it from
			// state, so the next apply recreates it instead of failing.
			PreConfig: func() {
				h.fake.with(func() {
					for id := range h.fake.projects {
						delete(h.fake.projects, id)
					}
				})
			},
			Config:           cfg,
			ConfigPlanChecks: expectAction(projectAddr, plancheck.ResourceActionCreate),
		},
	}, nil)
}

func TestUnitProjectProblemDetails(t *testing.T) {
	h := unitHarness(t)
	h.fake.fail("POST /api/projects", 409, "Conflict", "project name web is already in use")
	h.run([]resource.TestStep{{
		Config: h.config(`
resource "temps_project" "web" {
  name        = "web"
  source_type = "docker_image"
}`),
		ExpectError: regexp.MustCompile(`(?s)Unable to create Temps project.*HTTP 409 Conflict: project name web is\s+already in use`),
	}}, nil)
}

func TestUnitProjectForbiddenNamesPermission(t *testing.T) {
	h := unitHarness(t)
	h.fake.failRaw("POST /api/projects", 403,
		`{"title":"Insufficient Permissions","detail":"This operation requires the projects:create permission","required_permission":"projects:create"}`)
	h.run([]resource.TestStep{{
		Config: h.config(`
resource "temps_project" "web" {
  name        = "web"
  source_type = "docker_image"
}`),
		ExpectError: regexp.MustCompile(`(?s)missing the\s+"projects:create"\s+permission.*needs these permissions`),
	}}, nil)
}

func TestUnitProjectGitNeedsRepository(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: h.config(`
resource "temps_project" "web" {
  name = "web"
}`),
		ExpectError: regexp.MustCompile(`Missing Repository Information`),
	}}, nil)
}

func TestUnitProjectBuildSettingsNeedPreset(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: h.config(`
resource "temps_project" "web" {
  name          = "web"
  source_type   = "docker_image"
  build_command = "make"
}`),
		ExpectError: regexp.MustCompile(`preset is required with build settings`),
	}}, nil)
}
