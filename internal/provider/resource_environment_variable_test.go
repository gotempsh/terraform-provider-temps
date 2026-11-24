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

const (
	plainVarAddr  = "temps_environment_variable.plain"
	secretVarAddr = "temps_environment_variable.secret"
)

func envVarConfig(h *harness, plainValue, secretKey string, secret bool) string {
	return h.config(h.projectConfig("vars")+`
resource "temps_environment_variable" "plain" {
  project_id      = temps_project.parent.id
  key             = "LOG_LEVEL"
  value           = %q
  environment_ids = [data.temps_environment.production.id]
}

resource "temps_environment_variable" "secret" {
  project_id         = temps_project.parent.id
  key                = %q
  value              = "s3cr3t-value"
  is_secret          = %t
  include_in_preview = true
  environment_ids    = [data.temps_environment.production.id]
}`, plainValue, secretKey, secret)
}

func envVarSteps(h *harness) []resource.TestStep {
	return []resource.TestStep{
		{
			Config:           envVarConfig(h, "info", "API_TOKEN", true),
			ConfigPlanChecks: emptyPlanAfterApply(), // masked secret values cause no drift
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(plainVarAddr, "value", "info"),
				resource.TestCheckResourceAttr(plainVarAddr, "is_secret", "false"),
				resource.TestCheckResourceAttr(secretVarAddr, "value", "s3cr3t-value"),
				resource.TestCheckResourceAttr(secretVarAddr, "is_secret", "true"),
				resource.TestCheckResourceAttr(secretVarAddr, "environment_ids.#", "1"),
			),
		},
		{ // plain values import fully
			ResourceName: plainVarAddr, ImportState: true, ImportStateVerify: true,
			ImportStateIdFunc: stateID(plainVarAddr, "project_id", "id"),
		},
		{ // secret values are write-only: everything but the value imports
			ResourceName: secretVarAddr, ImportState: true, ImportStateVerify: true,
			ImportStateIdFunc:       stateID(secretVarAddr, "project_id", "id"),
			ImportStateVerifyIgnore: []string{"value"},
		},
		{ // update the plain value and rename the secret in place
			Config: envVarConfig(h, "debug", "API_KEY", true),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(plainVarAddr, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(secretVarAddr, plancheck.ResourceActionUpdate),
				},
				PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
			},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(plainVarAddr, "value", "debug"),
				resource.TestCheckResourceAttr(secretVarAddr, "key", "API_KEY"),
			),
		},
		{ // turning is_secret off replaces the variable
			Config:           envVarConfig(h, "debug", "API_KEY", false),
			ConfigPlanChecks: expectAction(secretVarAddr, plancheck.ResourceActionReplace),
			Check:            resource.TestCheckResourceAttr(secretVarAddr, "is_secret", "false"),
		},
	}
}

func (h *harness) envVarDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_environment_variable", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetEnvironmentVariable(context.Background(), mustID(rs.Primary.Attributes["project_id"]), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitEnvironmentVariable(t *testing.T) {
	h := unitHarness(t)
	h.run(envVarSteps(h), h.envVarDestroyed())
}

func TestAccEnvironmentVariable(t *testing.T) {
	h := accHarness(t)
	h.run(envVarSteps(h), h.envVarDestroyed())
}

// A plain value changed outside Terraform shows up as drift; a secret cannot.
func TestUnitEnvironmentVariableDrift(t *testing.T) {
	h := unitHarness(t)
	cfg := envVarConfig(h, "info", "API_TOKEN", true)
	h.run([]resource.TestStep{
		{Config: cfg},
		{
			PreConfig: func() {
				h.fake.with(func() {
					for _, v := range h.fake.vars {
						v.value = "changed-outside"
					}
				})
			},
			Config: cfg,
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(plainVarAddr, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(secretVarAddr, plancheck.ResourceActionNoop),
				},
			},
		},
	}, nil)
}

func TestUnitEnvironmentVariableWriteOnly(t *testing.T) {
	h := unitHarness(t)
	cfg := func(version int) string {
		return h.config(h.projectConfig("wo")+`
resource "temps_environment_variable" "secret" {
  project_id       = temps_project.parent.id
  key              = "API_TOKEN"
  value_wo         = "v%d-secret"
  value_wo_version = %d
  is_secret        = true
  environment_ids  = [data.temps_environment.production.id]
}`, version, version)
	}
	stored := func(want string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			var got string
			h.fake.with(func() {
				for _, v := range h.fake.vars {
					got = v.value
				}
			})
			if got != want {
				return errorf("server value = %q, want %q", got, want)
			}
			return nil
		}
	}
	h.run([]resource.TestStep{
		{
			Config: cfg(1), ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr(secretVarAddr, "value"),
				resource.TestCheckNoResourceAttr(secretVarAddr, "value_wo"),
				stored("v1-secret"),
			),
		},
		{
			Config: cfg(2), ConfigPlanChecks: expectAction(secretVarAddr, plancheck.ResourceActionUpdate),
			Check: stored("v2-secret"),
		},
	}, nil)
}

func TestUnitEnvironmentVariableNeedsValue(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: h.config(`
resource "temps_environment_variable" "x" {
  project_id      = 1
  key             = "A"
  environment_ids = [1]
}`),
		ExpectError: regexp.MustCompile(`Exactly one of value and value_wo is required`),
	}}, nil)
}

func TestUnitEnvironmentVariableKeyValidation(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: h.config(`
resource "temps_environment_variable" "x" {
  project_id      = 1
  key             = "1-BAD"
  value           = "x"
  environment_ids = [1]
}`),
		ExpectError: regexp.MustCompile(`must start with a letter or underscore`),
	}}, nil)
}
