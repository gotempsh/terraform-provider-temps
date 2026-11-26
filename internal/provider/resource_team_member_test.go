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

func TestUnitTeamMember(t *testing.T) {
	f := newMoreFake(t)
	cfg := func(user int, role string) string {
		return f.config(`
resource "temps_team_member" "m" {
  team_id = 3
  user_id = %d
  role    = %q
}`, user, role)
	}
	const addr = "temps_team_member.m"
	runEngine(t, []resource.TestStep{
		{Config: cfg(11, "superuser"), ExpectError: regexp.MustCompile(`(?s)role.*must be one of`)},
		{
			Config:           cfg(11, "viewer"),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(addr, "id", "3/11"),
				resource.TestCheckResourceAttr(addr, "user_email", "ada@example.com"),
				resource.TestCheckResourceAttr(addr, "user_name", "Ada"),
			),
		},
		{
			Config:           cfg(11, "admin"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
			Check: func(*terraform.State) error {
				b := f.body("PATCH /api/teams/3/members/11")
				if len(b) != 1 || b["role"] != "admin" {
					return fmt.Errorf("role update sent %v", b)
				}
				return nil
			},
		},
		{
			Config:           cfg(12, "admin"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionDestroyBeforeCreate),
			Check:            resource.TestCheckResourceAttr(addr, "id", "3/12"),
		},
		{ResourceName: addr, ImportState: true, ImportStateVerify: true},
		{ResourceName: addr, ImportState: true, ImportStateId: "12", ExpectError: regexp.MustCompile(`<team_id>/<user_id>`)},
		{
			PreConfig:        func() { f.with(func() { f.members["3/12"]["role"] = "owner" }) },
			Config:           cfg(12, "admin"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
		},
		{
			PreConfig:        func() { f.with(func() { delete(f.members, "3/12") }) },
			Config:           cfg(12, "admin"),
			ConfigPlanChecks: plannedCreate(addr),
		},
		{Config: cfg(12, "admin")},
		{
			// Adding the same user twice fails with Temps' conflict.
			Config: cfg(12, "admin") + `
resource "temps_team_member" "again" {
  team_id = 3
  user_id = 12
  role    = "viewer"
}`,
			ExpectError: regexp.MustCompile(`(?s)409.*already a member`),
		},
	}, func(*terraform.State) error {
		var n int
		f.with(func() { n = len(f.members) })
		if n != 0 {
			return fmt.Errorf("%d team members left after destroy", n)
		}
		return nil
	})
}
