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
)

func TestUnitProjectAccessService(t *testing.T) {
	f := newMoreFake(t)
	cfg := func(project, name string) string {
		return f.config(`
resource "temps_access_service" "api" {
  %s
  name            = %q
  enabled         = true
  max_ttl_seconds = 3600
  policy          = jsonencode({ origin = "https://api.example.com", methods = ["GET"] })
  credential      = "tok"
}`, project, name)
	}
	const addr = "temps_access_service.api"
	var id string
	runEngine(t, []resource.TestStep{
		{
			Config:           cfg("project_id = 8", "API"),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(addr, "project_id", "8"),
				resource.TestMatchResourceAttr(addr, "id", regexp.MustCompile(`^svc_\d+$`)),
				func(s *terraform.State) error {
					id = idOf(s, addr)
					if f.count("POST /api/projects/8/access-services") != 1 {
						return fmt.Errorf("the service was not created through the project: %v", f.requests)
					}
					return nil
				},
			),
		},
		{
			Config:           cfg("project_id = 8", "API v2"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
			Check: func(*terraform.State) error {
				if b := f.body("PUT /api/access-services/" + id); b["name"] != "API v2" || fmt.Sprint(b["expected_version"]) != "1" {
					return fmt.Errorf("update sent %v", b)
				}
				if f.count("GET /api/access-services/"+id) == 0 {
					return fmt.Errorf("reads must use the instance path")
				}
				return nil
			},
		},
		{ResourceName: addr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"credential"}},
		{
			// The ID is the bare service ID, which vaults refer to: the
			// project is read back.
			ResourceName: addr, ImportState: true,
			ImportStateCheck: func(s []*terraform.InstanceState) error {
				if len(s) != 1 || s[0].Attributes["project_id"] != "8" {
					return fmt.Errorf("import produced %v", s)
				}
				return nil
			},
		},
		{
			Config:           cfg("project_id = 9", "API v2"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionDestroyBeforeCreate),
		},
		{
			// Leaving the project out makes it a service of the instance.
			Config:           cfg("", "API v2"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionDestroyBeforeCreate),
			Check:            resource.TestMatchResourceAttr(addr, "id", regexp.MustCompile(`^svc_\d+$`)),
		},
		{
			PreConfig:        func() { f.with(func() { clear(f.services) }) },
			Config:           cfg("project_id = 9", "API v2"),
			ConfigPlanChecks: plannedCreate(addr),
		},
		{Config: cfg("project_id = 404", "API v2"), ExpectError: regexp.MustCompile(`(?s)404.*project`)},
	}, func(*terraform.State) error {
		var n int
		f.with(func() { n = len(f.services) })
		if n != 0 {
			return fmt.Errorf("%d access services left after destroy", n)
		}
		return nil
	})
}

func TestUnitProjectAccessVault(t *testing.T) {
	f := newMoreFake(t)
	cfg := func(project, name string) string {
		return f.config(`
resource "temps_access_vault" "v" {
  %s
  name        = %q
  service_ids = ["svc_a"]
}`, project, name)
	}
	const addr = "temps_access_vault.v"
	var id string
	runEngine(t, []resource.TestStep{
		{
			Config:           cfg("project_id = 8", "ops"),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: func(s *terraform.State) error {
				id = idOf(s, addr)
				if f.count("POST /api/projects/8/access-vaults") != 1 || len(f.links) != 1 {
					return fmt.Errorf("the vault was not created through the project: %v", f.requests)
				}
				return nil
			},
		},
		{
			Config:           cfg("project_id = 8", "ops-2"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
			Check: func(*terraform.State) error {
				if f.count("PUT /api/access-vaults/"+id) != 1 {
					return fmt.Errorf("update did not use the instance path: %v", f.requests)
				}
				return nil
			},
		},
		{ResourceName: addr, ImportState: true, ImportStateVerify: true},
		{
			PreConfig:        func() { f.with(func() { f.vaults[id]["service_ids"] = []any{"svc_b"} }) },
			Config:           cfg("project_id = 8", "ops-2"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
		},
		{
			// A vault of the whole instance keeps the instance paths.
			Config:           cfg("", "global"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionDestroyBeforeCreate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr(addr, "project_id"),
				resource.TestMatchResourceAttr(addr, "id", regexp.MustCompile(`^vault_\d+$`)),
			),
		},
	}, func(*terraform.State) error {
		var n int
		f.with(func() { n = len(f.vaults) })
		if n != 0 {
			return fmt.Errorf("%d vaults left after destroy", n)
		}
		return nil
	})
}
