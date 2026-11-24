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
	monitorAddr = "temps_monitor.web"
	pageAddr    = "temps_status_page.public"
	domainAddr  = "temps_status_page_domain.custom"
)

func monitorConfig(h *harness, name, checkPath string) string {
	return h.config(h.projectConfig("mon")+`
resource "temps_monitor" "web" {
  project_id     = temps_project.parent.id
  environment_id = data.temps_environment.production.id
  name           = %q
  monitor_type   = "health"
  check_path     = %q
}`, name, checkPath)
}

func monitorSteps(h *harness) []resource.TestStep {
	return []resource.TestStep{
		{
			Config:           monitorConfig(h, "web", "/healthz"),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(monitorAddr, "check_path", "/healthz"),
				resource.TestCheckResourceAttr(monitorAddr, "check_interval_seconds", "60"),
				resource.TestMatchResourceAttr(monitorAddr, "monitor_url", regexp.MustCompile(`/healthz$`)),
			),
		},
		{ResourceName: monitorAddr, ImportState: true, ImportStateVerify: true},
		{
			Config:           monitorConfig(h, "web", "/ready"),
			ConfigPlanChecks: expectAction(monitorAddr, plancheck.ResourceActionUpdate),
			Check:            resource.TestCheckResourceAttr(monitorAddr, "check_path", "/ready"),
		},
		{
			Config:           monitorConfig(h, "web-renamed", "/ready"),
			ConfigPlanChecks: expectAction(monitorAddr, plancheck.ResourceActionReplace),
			Check:            resource.TestCheckResourceAttr(monitorAddr, "name", "web-renamed"),
		},
	}
}

func (h *harness) monitorDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_monitor", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetMonitor(context.Background(), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitMonitor(t *testing.T) {
	h := unitHarness(t)
	h.run(monitorSteps(h), h.monitorDestroyed())
}

func TestAccMonitor(t *testing.T) {
	h := accHarness(t)
	h.run(monitorSteps(h), h.monitorDestroyed())
}

func TestUnitMonitorValidation(t *testing.T) {
	h := unitHarness(t)
	for _, tc := range []struct{ body, want string }{
		{`check_path = "healthz"`, `must start with '/'`},
		{`check_path = "/health?x=1"`, `must not contain a query string`},
		{`check_interval_seconds = 10`, `value must be between 30 and 86400`},
		{`monitor_type = "tcp"`, `value must be one of`},
	} {
		h.run([]resource.TestStep{{
			Config: h.config(`
resource "temps_monitor" "web" {
  project_id     = 1
  environment_id = 1
  name           = "web"
  ` + tc.body + `
}`),
			ExpectError: regexp.MustCompile(tc.want),
		}}, nil)
	}
}

func statusPageConfig(h *harness, title string, published bool, displayName, theme string) string {
	return h.config(h.projectConfig("page")+`
resource "temps_monitor" "web" {
  project_id     = temps_project.parent.id
  environment_id = data.temps_environment.production.id
  name           = "web"
}

resource "temps_monitor" "api" {
  project_id     = temps_project.parent.id
  environment_id = data.temps_environment.production.id
  name           = "api"
  check_path     = "/api/health"
}

resource "temps_status_page" "public" {
  title       = %q
  description = "Status of the example services."
  published   = %t

  monitors = [
    { monitor_id = temps_monitor.web.id, display_name = %q, group_name = "Core" },
    { monitor_id = temps_monitor.api.id, display_name = "API" },
  ]

  appearance = {
    theme                  = %q
    show_uptime_percentage = true
  }
}`, title, published, displayName, theme)
}

func statusPageSteps(h *harness) []resource.TestStep {
	return []resource.TestStep{
		{
			Config:           statusPageConfig(h, "Example status", true, "Web", "auto"),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(pageAddr, "published", "true"),
				resource.TestCheckResourceAttrSet(pageAddr, "slug"),
				resource.TestCheckResourceAttr(pageAddr, "monitors.#", "2"),
				resource.TestCheckResourceAttr(pageAddr, "monitors.0.display_name", "Web"),
				resource.TestCheckResourceAttr(pageAddr, "monitors.0.group_name", "Core"),
				resource.TestCheckResourceAttr(pageAddr, "monitors.1.display_name", "API"),
				resource.TestCheckResourceAttr(pageAddr, "appearance.theme", "auto"),
			),
		},
		{ResourceName: pageAddr, ImportState: true, ImportStateVerify: true},
		{
			Config:           statusPageConfig(h, "Example status page", false, "Website", "dark"),
			ConfigPlanChecks: expectAction(pageAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(pageAddr, "title", "Example status page"),
				resource.TestCheckResourceAttr(pageAddr, "published", "false"),
				resource.TestCheckResourceAttr(pageAddr, "monitors.0.display_name", "Website"),
				resource.TestCheckResourceAttr(pageAddr, "appearance.theme", "dark"),
			),
		},
	}
}

func (h *harness) statusPageDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_status_page", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetStatusPage(context.Background(), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitStatusPage(t *testing.T) {
	h := unitHarness(t)
	h.run(statusPageSteps(h), h.statusPageDestroyed())
}

func TestAccStatusPage(t *testing.T) {
	h := accHarness(t)
	h.run(statusPageSteps(h), h.statusPageDestroyed())
}

func pagePasswordConfig(h *harness, version int) string {
	return h.config(h.projectConfig("pw")+`
resource "temps_monitor" "web" {
  project_id     = temps_project.parent.id
  environment_id = data.temps_environment.production.id
  name           = "web"
}

resource "temps_status_page" "public" {
  title                 = "Private status"
  monitors              = [{ monitor_id = temps_monitor.web.id, display_name = "Web" }]
  password_wo           = "v%d-password"
  password_wo_version   = %d
  subscriptions_enabled = true
}`, version, version)
}

func TestUnitStatusPagePasswordAndSubscriptions(t *testing.T) {
	h := unitHarness(t)
	password := func(want string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			var got string
			h.fake.with(func() {
				for _, p := range h.fake.pages {
					got = p.password
				}
			})
			if got != want {
				return errorf("server password = %q, want %q", got, want)
			}
			return nil
		}
	}
	h.run([]resource.TestStep{
		{
			Config: pagePasswordConfig(h, 1), ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				password("v1-password"),
				resource.TestCheckNoResourceAttr(pageAddr, "password_wo"),
				resource.TestCheckResourceAttr(pageAddr, "subscriptions_enabled", "true"),
			),
		},
		{Config: pagePasswordConfig(h, 2), Check: password("v2-password")},
		{
			Config: h.config(h.projectConfig("pw") + `
resource "temps_monitor" "web" {
  project_id     = temps_project.parent.id
  environment_id = data.temps_environment.production.id
  name           = "web"
}

resource "temps_status_page" "public" {
  title    = "Private status"
  monitors = [{ monitor_id = temps_monitor.web.id, display_name = "Web" }]
}`),
			Check: password(""),
		},
	}, h.statusPageDestroyed())
}

// Servers without the password endpoint answer 404 with no body: the
// diagnostic says the server is too old instead of "not found".
func TestUnitStatusPagePasswordUnsupported(t *testing.T) {
	h := unitHarness(t)
	h.fake.noPagePasswd = true
	h.run([]resource.TestStep{{
		Config:      pagePasswordConfig(h, 1),
		ExpectError: regexp.MustCompile(`(?s)does not provide this endpoint`),
	}}, nil)
}

func domainConfig(h *harness, hostname string) string {
	return h.config(h.projectConfig("dom")+`
resource "temps_monitor" "web" {
  project_id     = temps_project.parent.id
  environment_id = data.temps_environment.production.id
  name           = "web"
}

resource "temps_status_page" "public" {
  title    = "Example status"
  monitors = [{ monitor_id = temps_monitor.web.id, display_name = "Web" }]
}

resource "temps_status_page_domain" "custom" {
  status_page_id = temps_status_page.public.id
  hostname       = %q
}`, hostname)
}

func domainSteps(h *harness) []resource.TestStep {
	host := h.prefix + ".status.example.com"
	return []resource.TestStep{
		{
			Config:           domainConfig(h, host),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(domainAddr, "hostname", host),
				resource.TestCheckResourceAttr(domainAddr, "verification_method", "http-01"),
				resource.TestCheckResourceAttrSet(domainAddr, "status"),
			),
		},
		{
			ResourceName: domainAddr, ImportState: true, ImportStateVerify: true,
			ImportStateIdFunc: stateID(domainAddr, "status_page_id", "id"),
		},
		{
			// Only verify_on_apply is updatable (it is provider-side).
			Config: h.config(h.projectConfig("dom")+`
resource "temps_monitor" "web" {
  project_id     = temps_project.parent.id
  environment_id = data.temps_environment.production.id
  name           = "web"
}

resource "temps_status_page" "public" {
  title    = "Example status"
  monitors = [{ monitor_id = temps_monitor.web.id, display_name = "Web" }]
}

resource "temps_status_page_domain" "custom" {
  status_page_id  = temps_status_page.public.id
  hostname        = %q
  verify_on_apply = true
}`, host),
			ConfigPlanChecks: expectAction(domainAddr, plancheck.ResourceActionUpdate),
		},
		{
			Config:           domainConfig(h, "www."+host),
			ConfigPlanChecks: expectAction(domainAddr, plancheck.ResourceActionReplace),
			Check:            resource.TestCheckResourceAttr(domainAddr, "hostname", "www."+host),
		},
	}
}

func (h *harness) domainDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_status_page_domain", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetStatusPageDomain(context.Background(), mustID(rs.Primary.Attributes["status_page_id"]), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitStatusPageDomain(t *testing.T) {
	h := unitHarness(t)
	h.run(domainSteps(h), h.domainDestroyed())
}

func TestAccStatusPageDomain(t *testing.T) {
	h := accHarness(t)
	h.run(domainSteps(h), h.domainDestroyed())
}
