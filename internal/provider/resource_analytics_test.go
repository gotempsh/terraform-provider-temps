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

func TestUnitAnalyticsGoal(t *testing.T) {
	f := newMoreFake(t)
	event := f.config(`
resource "temps_analytics_goal" "g" {
  project_id       = 8
  name             = "Upgrade"
  kind             = "event"
  event_name       = "plan_upgraded"
  event_filter     = jsonencode([{ type = "custom_data", value = { path = "plan", value = "pro" } }])
  revenue_property = "amount"
  revenue_currency = "EUR"
}`)
	page := f.config(`
resource "temps_analytics_goal" "g" {
  project_id = 8
  name       = "Thank you page"
  kind       = "page"
  page_path  = "/thank-you"
}`)
	const addr = "temps_analytics_goal.g"
	var id int64
	runEngine(t, []resource.TestStep{
		{
			Config:           event,
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: func(s *terraform.State) error {
				id = mustID(idOf(s, addr)[2:])
				return nil
			},
		},
		{
			// Switching to a page goal clears the event's fields.
			Config:           page,
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
			Check: func(*terraform.State) error {
				b := f.body(fmt.Sprintf("PUT /api/projects/8/analytics/goals/%d", id))
				for _, k := range []string{"event_name", "revenue_property", "revenue_currency"} {
					if v, ok := b[k]; !ok || v != nil {
						return fmt.Errorf("update must send %s: null, sent %v", k, b)
					}
				}
				return nil
			},
		},
		{ResourceName: addr, ImportState: true, ImportStateVerify: true},
		{
			PreConfig:        func() { f.with(func() { f.goals[id]["page_path"] = "/done" }) },
			Config:           page,
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
		},
		{
			PreConfig:        func() { f.with(func() { delete(f.goals, id) }) },
			Config:           page,
			ConfigPlanChecks: plannedCreate(addr),
		},
		{Config: page},
		{
			Config: page + `
resource "temps_analytics_goal" "dup" {
  project_id = 8
  name       = "Thank you page"
  kind       = "page"
  page_path  = "/thanks"
}`,
			ExpectError: regexp.MustCompile(`(?s)409.*already exists`),
		},
	}, func(*terraform.State) error {
		var n int
		f.with(func() { n = len(f.goals) })
		if n != 0 {
			return fmt.Errorf("%d goals left after destroy", n)
		}
		return nil
	})
}

func TestUnitAnalyticsTrafficFilter(t *testing.T) {
	f := newMoreFake(t)
	// Enough filters that the new one is not on the first page.
	f.with(func() {
		for i := 0; i < 130; i++ {
			id := f.next()
			f.filters[id] = map[string]any{"id": id, "project_id": int64(8), "kind": "ip", "value": fmt.Sprintf("198.51.100.%d", i)}
		}
	})
	cfg := func(value string) string {
		return f.config(`
resource "temps_analytics_traffic_filter" "spam" {
  project_id = 8
  kind       = "referrer"
  value      = %q
  note       = "referral spam"
}`, value)
	}
	const addr = "temps_analytics_traffic_filter.spam"
	runEngine(t, []resource.TestStep{
		{
			// Temps stores the domain; the configured URL is kept.
			Config:           cfg("https://www.Spam.example/landing"),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(addr, "normalized_value", "spam.example"),
				resource.TestCheckResourceAttr(addr, "value", "https://www.Spam.example/landing"),
			),
		},
		{ResourceName: addr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"value"}},
		{
			Config:           cfg("spam2.example"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionDestroyBeforeCreate),
			Check:            resource.TestCheckResourceAttr(addr, "normalized_value", "spam2.example"),
		},
		{
			PreConfig: func() {
				f.with(func() {
					for k, v := range f.filters {
						if v["kind"] == "referrer" {
							delete(f.filters, k)
						}
					}
				})
			},
			Config:           cfg("spam2.example"),
			ConfigPlanChecks: plannedCreate(addr),
		},
		{Config: cfg("spam2.example")},
		{Config: f.config(`
resource "temps_analytics_traffic_filter" "bad" {
  project_id = 8
  kind       = "country"
  value      = "Spain"
}`), ExpectError: regexp.MustCompile(`(?s)400.*not a valid country`)},
	}, func(*terraform.State) error {
		var n int
		f.with(func() {
			for _, v := range f.filters {
				if v["kind"] == "referrer" {
					n++
				}
			}
		})
		if n != 0 {
			return fmt.Errorf("%d referrer filters left after destroy", n)
		}
		return nil
	})
}

func TestUnitProjectAnalyticsSettings(t *testing.T) {
	f := newMoreFake(t)
	cfg := func(dc, spam bool) string {
		return f.config(`
resource "temps_project_analytics_settings" "s" {
  project_id                 = 8
  exclude_datacenter_traffic = %t
  exclude_spam_referrers     = %t
}`, dc, spam)
	}
	const addr = "temps_project_analytics_settings.s"
	var patches int
	runEngine(t, []resource.TestStep{
		{
			Config:           cfg(true, true),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(addr, "id", "8"),
				resource.TestCheckResourceAttr(addr, "datacenter_detection_available", "true"),
			),
		},
		{Config: cfg(true, false), ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate)},
		{ResourceName: addr, ImportState: true, ImportStateId: "8", ImportStateVerify: true},
		{
			PreConfig:        func() { f.with(func() { f.settings[8]["exclude_datacenter_traffic"] = false }) },
			Config:           cfg(true, false),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
			Check: func(*terraform.State) error {
				patches = f.count("PATCH /api/projects/8/analytics/settings")
				return nil
			},
		},
		{Config: f.config(`
resource "temps_project_analytics_settings" "s" {
  project_id             = 404
  exclude_spam_referrers = true
}`), ExpectError: regexp.MustCompile(`(?s)404.*project`)},
	}, func(*terraform.State) error {
		// Destroying only stops managing the settings.
		if n := f.count("PATCH /api/projects/8/analytics/settings"); n != patches {
			return fmt.Errorf("destroy changed the settings (%d PATCH, %d before)", n, patches)
		}
		var dc any
		f.with(func() { dc = f.settings[8]["exclude_datacenter_traffic"] })
		if dc != true {
			return fmt.Errorf("settings were reset on destroy")
		}
		return nil
	})
}
