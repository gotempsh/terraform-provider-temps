// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const funnelAddr = "temps_funnel.signup"

func funnelConfig(f *moreFake) string {
	return f.config(`
resource "temps_funnel" "signup" {
  project_id  = 8
  name        = "Signup"
  description = "Visit to signup"
  steps       = jsonencode([{ event_name = "page_view", event_filter = [] }, { event_name = "signup" }])
}`)
}

func funnelsGone(f *moreFake) resource.TestCheckFunc {
	return func(*terraform.State) error {
		var n int
		f.with(func() {
			for _, fn := range f.funnels {
				if fn["name"] == "Signup" {
					n++
				}
			}
		})
		if n != 0 {
			return fmt.Errorf("%d funnels left after destroy", n)
		}
		return nil
	}
}

// Temps from 2026-11-25 on pages the funnel list and returns the funnel from
// writes; released servers sent the whole list as an array and a message.
// Either way the funnel is found, also behind newer ones on a later page.
func TestUnitFunnelBothListShapes(t *testing.T) {
	for _, tc := range []struct{ name, list string }{{"page", ""}, {"array", "array"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMoreFake(t)
			f.funnelList = tc.list
			cfg := funnelConfig(f)
			var id int64
			runEngine(t, []resource.TestStep{
				{
					Config:           cfg,
					ConfigPlanChecks: emptyPlanAfterApply(),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr(funnelAddr, "name", "Signup"),
						resource.TestCheckResourceAttr(funnelAddr, "is_active", "true"),
						func(s *terraform.State) error {
							id = mustID(idOf(s, funnelAddr)[2:])
							return nil
						},
					),
				},
				{
					// 250 newer funnels: pages of 100 put this one on page 3.
					PreConfig: func() {
						f.with(func() {
							for i := range 250 {
								n := f.next()
								f.funnels[n] = map[string]any{"id": n, "project_id": int64(8), "name": fmt.Sprintf("Newer %d", i), "is_active": true}
							}
						})
					},
					Config:   cfg,
					PlanOnly: true,
				},
				{ResourceName: funnelAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"steps"}},
				{
					PreConfig:        func() { f.with(func() { delete(f.funnels, id) }) },
					Config:           cfg,
					ConfigPlanChecks: plannedCreate(funnelAddr),
				},
				{Config: cfg, ConfigPlanChecks: emptyPlanAfterApply()},
			}, funnelsGone(f))
		})
	}
}

// An answer that is neither shape fails the refresh instead of reading as
// an empty list, which would drop the funnel from state.
func TestUnitFunnelRejectsAnotherListShape(t *testing.T) {
	f := newMoreFake(t)
	cfg := funnelConfig(f)
	runEngine(t, []resource.TestStep{
		{Config: cfg},
		{
			PreConfig:   func() { f.with(func() { f.funnelList = `{"data":[]}` }) },
			Config:      cfg,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`(?s)neither a list nor a page\s+with\s+"items"`),
		},
		{
			PreConfig:        func() { f.with(func() { f.funnelList = "" }) },
			Config:           cfg,
			ConfigPlanChecks: emptyPlanAfterApply(),
		},
	}, funnelsGone(f))
}
