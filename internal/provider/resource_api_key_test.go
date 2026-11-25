// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

const apiKeyAddr = "temps_api_key.ci"

func apiKeyConfig(h *harness, name, perms, extra string) string {
	return h.config(h.projectConfig("apikey") + fmt.Sprintf(`
resource "temps_api_key" "ci" {
  name        = %q
  role        = "custom"
  permissions = %s
  project_id  = temps_project.parent.id
  %s
}`, h.name(name), perms, extra))
}

// keyWorks checks against a real server that the minted key authenticates.
func (h *harness) keyWorks() resource.TestCheckFunc {
	return func(s *terraform.State) error {
		if h.fake != nil {
			return nil
		}
		rs, ok := s.RootModule().Resources[apiKeyAddr]
		if !ok {
			return fmt.Errorf("%s not in state", apiKeyAddr)
		}
		c, err := client.New(client.Config{Endpoint: os.Getenv(envEndpoint), APIKey: rs.Primary.Attributes["key"]})
		if err != nil {
			return err
		}
		_, err = c.GetProject(context.Background(), mustID(rs.Primary.Attributes["project_id"]))
		return err
	}
}

func apiKeySteps(h *harness) []resource.TestStep {
	future := time.Now().AddDate(0, 6, 0).UTC().Truncate(time.Second).Format(time.RFC3339)
	return []resource.TestStep{
		{
			Config:           apiKeyConfig(h, "ci", `["projects:read"]`, ""),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet(apiKeyAddr, "key"),
				resource.TestCheckResourceAttrSet(apiKeyAddr, "expires_at"),
				resource.TestCheckResourceAttr(apiKeyAddr, "enabled", "true"),
				h.keyWorks(),
			),
		},
		// Rename, set an expiry and deactivate in place.
		{
			Config:           apiKeyConfig(h, "ci-renamed", `["projects:read"]`, fmt.Sprintf("expires_at = %q\n  enabled = false", future)),
			ConfigPlanChecks: expectAction(apiKeyAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(apiKeyAddr, "enabled", "false"),
				resource.TestCheckResourceAttr(apiKeyAddr, "expires_at", future),
			),
		},
		// Temps cannot change permissions: the key is replaced.
		{
			Config:           apiKeyConfig(h, "ci-renamed", `["projects:read", "deployments:read"]`, ""),
			ConfigPlanChecks: expectAction(apiKeyAddr, plancheck.ResourceActionDestroyBeforeCreate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(apiKeyAddr, "permissions.#", "2"),
				resource.TestCheckResourceAttr(apiKeyAddr, "enabled", "true"),
				h.keyWorks(),
			),
		},
		{ResourceName: apiKeyAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"key"}},
	}
}

func (h *harness) apiKeysDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_api_key", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetAPIKey(context.Background(), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitAPIKey(t *testing.T) {
	h := unitHarness(t)
	h.run(apiKeySteps(h), h.apiKeysDestroyed())
}

func TestAccAPIKey(t *testing.T) {
	h := accHarness(t)
	h.run(apiKeySteps(h), h.apiKeysDestroyed())
}

func TestUnitAPIKeyValidation(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{
		{
			Config:      h.config(`resource "temps_api_key" "k" {` + "\n" + `  name = "k"` + "\n" + `  role = "custom"` + "\n}"),
			ExpectError: regexp.MustCompile(`needs permissions`),
		},
		{
			Config:      h.config(`resource "temps_api_key" "k" {` + "\n" + `  name = "k"` + "\n" + `  role = "user"` + "\n" + `  permissions = ["projects:read"]` + "\n}"),
			ExpectError: regexp.MustCompile(`role = "custom"`),
		},
		{
			Config:      h.config(`resource "temps_api_key" "k" {` + "\n" + `  name = "k"` + "\n" + `  role = "reader"` + "\n" + `  expires_at = "2020-01-01T00:00:00Z"` + "\n}"),
			ExpectError: regexp.MustCompile(`in the past`),
		},
	}, nil)
}
