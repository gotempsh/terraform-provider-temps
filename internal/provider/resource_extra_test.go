// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

// validationCase checks that a configuration fails at plan time with want.
func validationSteps(h *harness, base string, cases []struct{ body, want string }) []resource.TestStep {
	steps := make([]resource.TestStep, len(cases))
	for i, tc := range cases {
		steps[i] = resource.TestStep{
			Config: h.config(fmt.Sprintf(base, tc.body)), PlanOnly: true, ExpectError: regexp.MustCompile(tc.want),
		}
	}
	return steps
}

// ------------------------------------------------------------ custom domain

const customDomainAddr = "temps_custom_domain.app"

func customDomainConfig(h *harness, extra string) string {
	return h.config(h.projectConfig("dom")+`
resource "temps_custom_domain" "app" {
  project_id     = temps_project.parent.id
  environment_id = data.temps_environment.production.id
  domain         = %q
  %s
}`, h.name("app")+".example.com", extra)
}

func customDomainSteps(h *harness) []resource.TestStep {
	return []resource.TestStep{
		{
			Config:           customDomainConfig(h, ""),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(customDomainAddr, "status", "pending"),
				resource.TestCheckNoResourceAttr(customDomainAddr, "redirect_to"),
			),
		},
		{ResourceName: customDomainAddr, ImportState: true, ImportStateVerify: true, ImportStateIdFunc: stateID(customDomainAddr, "project_id", "id")},
		{
			Config:           customDomainConfig(h, `redirect_to = "https://www.example.com"`+"\n  status_code = 308"),
			ConfigPlanChecks: expectAction(customDomainAddr, plancheck.ResourceActionUpdate),
			Check:            resource.TestCheckResourceAttr(customDomainAddr, "status_code", "308"),
		},
		{
			// The API cannot clear a status code, so removing it replaces the domain.
			Config:           customDomainConfig(h, `redirect_to = "https://www.example.com"`),
			ConfigPlanChecks: expectAction(customDomainAddr, plancheck.ResourceActionReplace),
			Check:            resource.TestCheckNoResourceAttr(customDomainAddr, "status_code"),
		},
		{
			Config:           customDomainConfig(h, ""),
			ConfigPlanChecks: expectAction(customDomainAddr, plancheck.ResourceActionUpdate),
			Check:            resource.TestCheckNoResourceAttr(customDomainAddr, "redirect_to"),
		},
	}
}

func (h *harness) customDomainsDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_custom_domain", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetCustomDomain(context.Background(), mustID(rs.Primary.Attributes["project_id"]), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitCustomDomain(t *testing.T) {
	h := unitHarness(t)
	h.run(customDomainSteps(h), h.customDomainsDestroyed())
}

func TestAccCustomDomain(t *testing.T) {
	h := accHarness(t)
	h.run(customDomainSteps(h), h.customDomainsDestroyed())
}

func TestUnitCustomDomainValidation(t *testing.T) {
	h := unitHarness(t)
	base := `resource "temps_custom_domain" "x" {
  project_id     = 1
  environment_id = 2
  %s
}`
	h.run(validationSteps(h, base, []struct{ body, want string }{
		{`domain = "App.Example.com"`, `lower-case hostname`},
		{`domain = "app.example.com."`, `lower-case hostname`},
		{`domain = "https://app.example.com"`, `lower-case hostname`},
		{"domain = \"app.example.com\"\n  status_code = 301", `"redirect_to" must be specified`},
		{"domain = \"app.example.com\"\n  redirect_to = \"https://x.example.com\"\n  status_code = 200", `value must be one of`},
	}), nil)
}

// ---------------------------------------------------------------- webhook

const webhookAddr = "temps_webhook.deploys"

func webhookConfig(h *harness, body string) string {
	return h.config(h.projectConfig("hook")+`
resource "temps_webhook" "deploys" {
  project_id = temps_project.parent.id
  url        = "https://example.com/hooks/%s"
  %s
}`, h.name("deploys"), body)
}

func webhookSteps(h *harness) []resource.TestStep {
	return []resource.TestStep{
		{
			Config:           webhookConfig(h, `events = ["deployment.succeeded", "deployment.failed"]`+"\n  secret = \"0123456789abcdef-signing\""),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(webhookAddr, "has_secret", "true"),
				resource.TestCheckResourceAttr(webhookAddr, "enabled", "true"),
				resource.TestCheckResourceAttr(webhookAddr, "events.#", "2"),
			),
		},
		{ResourceName: webhookAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"secret"},
			ImportStateIdFunc: stateID(webhookAddr, "project_id", "id")},
		{
			Config:           webhookConfig(h, `events = ["deployment.failed"]`+"\n  enabled = false\n  secret = \"0123456789abcdef-signing\""),
			ConfigPlanChecks: expectAction(webhookAddr, plancheck.ResourceActionUpdate),
			Check:            resource.TestCheckResourceAttr(webhookAddr, "enabled", "false"),
		},
		{
			// A secret cannot be removed in place.
			Config:           webhookConfig(h, `events = ["deployment.failed"]`),
			ConfigPlanChecks: expectAction(webhookAddr, plancheck.ResourceActionReplace),
			Check:            resource.TestCheckResourceAttr(webhookAddr, "has_secret", "false"),
		},
		{
			Config: h.config(`data "temps_webhook_event_types" "all" {}`),
			Check:  resource.TestCheckTypeSetElemAttr("data.temps_webhook_event_types.all", "event_types.*", "deployment.succeeded"),
		},
	}
}

func (h *harness) webhooksDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_webhook", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetWebhook(context.Background(), mustID(rs.Primary.Attributes["project_id"]), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitWebhook(t *testing.T) {
	h := unitHarness(t)
	h.run(webhookSteps(h), h.webhooksDestroyed())
}

func TestAccWebhook(t *testing.T) {
	h := accHarness(t)
	h.run(webhookSteps(h), h.webhooksDestroyed())
}

func TestUnitWebhookValidation(t *testing.T) {
	h := unitHarness(t)
	base := `resource "temps_webhook" "x" {
  project_id = 1
  %s
}`
	h.run(validationSteps(h, base, []struct{ body, want string }{
		{"url = \"https://user:pass@example.com/h\"\n  events = [\"deployment.failed\"]", `credentials in the URL`},
		{"url = \"ftp://example.com/h\"\n  events = [\"deployment.failed\"]", `scheme must be http or https`},
		{"url = \"/relative\"\n  events = [\"deployment.failed\"]", `scheme must be http or https`},
		{"url = \"https://example.com/h\"\n  events = []", `set must contain at least 1 elements`},
		{"url = \"https://example.com/h\"\n  events = [\"deployment.failed\"]\n  secret = \"\"", `string length must be at least 1`},
	}), nil)
}

// ----------------------------------------------------------- feature flags

const (
	flagAddr    = "temps_feature_flag.checkout"
	flagEnvAddr = "temps_feature_flag_environment.checkout_prod"
)

func flagConfig(h *harness, valueType, defaultValue, envBlock string) string {
	return h.config(h.projectConfig("flag")+`
resource "temps_feature_flag" "checkout" {
  project_id    = temps_project.parent.id
  key           = "checkout.config"
  value_type    = %q
  default_value = %s
  description   = "Checkout tuning"
}
%s`, valueType, defaultValue, envBlock)
}

const flagEnvBlock = `
resource "temps_feature_flag_environment" "checkout_prod" {
  project_id     = temps_project.parent.id
  key            = temps_feature_flag.checkout.key
  environment_id = data.temps_environment.production.id
  %s
}`

func flagSteps(h *harness) []resource.TestStep {
	// Keys and spacing differ from what the server echoes back; the plan
	// after apply must still be empty.
	jsonDefault := `"{ \"retries\": 3, \"backoff\": [1.50, 2] }"`
	return []resource.TestStep{
		{
			Config:           flagConfig(h, "json", jsonDefault, fmt.Sprintf(flagEnvBlock, `value = jsonencode({ retries = 5 })`)),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(flagAddr, "client_visible", "false"),
				resource.TestCheckResourceAttr(flagEnvAddr, "enabled", "true"),
			),
		},
		// An import reads the server's spelling of the JSON value.
		{ResourceName: flagAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"default_value"},
			ImportStateIdFunc: stateID(flagAddr, "project_id", "key")},
		{ResourceName: flagEnvAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"value"},
			ImportStateIdFunc: stateID(flagEnvAddr, "project_id", "key", "environment_id")},
		{
			Config:           flagConfig(h, "json", `jsonencode({ retries = 1 })`, fmt.Sprintf(flagEnvBlock, "enabled = false")),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(flagEnvAddr, "enabled", "false"),
				resource.TestCheckNoResourceAttr(flagEnvAddr, "value"),
			),
		},
		// Destroying the flag archives it; declaring it again restores it
		// instead of failing on the reserved key.
		{Config: h.config(h.projectConfig("flag"))},
		{
			Config:           flagConfig(h, "json", `jsonencode({ retries = 2 })`, ""),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check:            resource.TestCheckResourceAttr(flagAddr, "default_value", `{"retries":2}`),
		},
		{
			// The override set before archiving must not come back with the restore.
			Config:           flagConfig(h, "json", `jsonencode({ retries = 2 })`, fmt.Sprintf(flagEnvBlock, "")),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(flagEnvAddr, "enabled", "true"),
				resource.TestCheckNoResourceAttr(flagEnvAddr, "value"),
			),
		},
		{
			// An archived key cannot change type.
			Config:      flagConfig(h, "bool", `jsonencode(true)`, ""),
			ExpectError: regexp.MustCompile(`archived as a json flag`),
		},
	}
}

func (h *harness) flagsArchived() resource.TestCheckFunc {
	return func(s *terraform.State) error {
		for _, rs := range s.RootModule().Resources {
			if rs.Type != "temps_feature_flag" {
				continue
			}
			f, err := h.client.GetFeatureFlag(context.Background(), mustID(rs.Primary.Attributes["project_id"]), rs.Primary.Attributes["key"])
			if err == nil && f.ArchivedAt == nil {
				return fmt.Errorf("feature flag %s is not archived after destroy", rs.Primary.ID)
			}
		}
		return nil
	}
}

func TestUnitFeatureFlag(t *testing.T) {
	h := unitHarness(t)
	h.run(flagSteps(h), h.flagsArchived())
}

func TestAccFeatureFlag(t *testing.T) {
	h := accHarness(t)
	h.run(flagSteps(h), h.flagsArchived())
}

func TestUnitFeatureFlagValidation(t *testing.T) {
	h := unitHarness(t)
	base := `resource "temps_feature_flag" "x" {
  project_id = 1
  %s
}`
	h.run(validationSteps(h, base, []struct{ body, want string }{
		{"key = \"New Checkout\"\n  value_type = \"bool\"\n  default_value = \"true\"", `must match \[a-z0-9\]`},
		{"key = \"ok\"\n  value_type = \"bool\"\n  default_value = jsonencode(\"yes\")", `is "yes", which is not a bool value`},
		{"key = \"ok\"\n  value_type = \"number\"\n  default_value = \"not json\"", `must be a JSON document`},
		{"key = \"ok\"\n  value_type = \"json\"\n  default_value = \"null\"", `which is not a json value`},
		{"key = \"ok\"\n  value_type = \"date\"\n  default_value = \"1\"", `value must be one of`},
	}), nil)
}

// ------------------------------------------------------- IP access policy

const (
	ipProjectAddr = "temps_ip_access_policy.project"
	ipEnvAddr     = "temps_ip_access_policy.production"
)

func ipAccessConfig(h *harness, projectBody, envBody string) string {
	cfg := h.projectConfig("ip") + fmt.Sprintf(`
resource "temps_ip_access_policy" "project" {
  project_id = temps_project.parent.id
  %s
}`, projectBody)
	if envBody != "" {
		cfg += fmt.Sprintf(`
resource "temps_ip_access_policy" "production" {
  project_id     = temps_project.parent.id
  environment_id = data.temps_environment.production.id
  %s
}`, envBody)
	}
	return h.config(cfg)
}

const ipDenyList = `mode = "deny"
  cidrs = [
    { cidr = "203.0.113.0/24", description = "Scanner" },
    { cidr = "2001:db8::/32" },
  ]`

func ipAccessSteps(h *harness) []resource.TestStep {
	allow := `mode  = "allow"
  cidrs = [{ cidr = "198.51.100.0/24", description = "Office" }]`
	return []resource.TestStep{
		{
			Config:           ipAccessConfig(h, ipDenyList, `mode = "disabled"`),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(ipProjectAddr, "cidrs.#", "2"),
				resource.TestCheckResourceAttr(ipProjectAddr, "cidrs.0.description", "Scanner"),
				resource.TestCheckResourceAttr(ipEnvAddr, "cidrs.#", "0"),
			),
		},
		{ResourceName: ipProjectAddr, ImportState: true, ImportStateVerify: true, ImportStateIdFunc: stateID(ipProjectAddr, "project_id")},
		{ResourceName: ipEnvAddr, ImportState: true, ImportStateVerify: true, ImportStateIdFunc: stateID(ipEnvAddr, "project_id", "environment_id")},
		{
			// The test client is not on the allow list, so Temps refuses it...
			Config:      ipAccessConfig(h, allow, ""),
			ExpectError: regexp.MustCompile(`acknowledge_lockout = true`),
		},
		{
			// ...until the lockout is acknowledged.
			Config:           ipAccessConfig(h, allow+"\n  acknowledge_lockout = true", ""),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check:            resource.TestCheckResourceAttr(ipProjectAddr, "mode", "allow"),
		},
	}
}

func (h *harness) ipAccessDeleted() resource.TestCheckFunc {
	return func(s *terraform.State) error {
		for _, rs := range s.RootModule().Resources {
			if rs.Type != "temps_ip_access_policy" {
				continue
			}
			var env int64
			if v := rs.Primary.Attributes["environment_id"]; v != "" {
				env = mustID(v)
			}
			p, err := h.client.GetIPAccessPolicy(context.Background(), mustID(rs.Primary.Attributes["project_id"]), env)
			if err == nil && p.Configured {
				return fmt.Errorf("IP access policy %s still configured after destroy", rs.Primary.ID)
			}
		}
		return nil
	}
}

func TestUnitIPAccessPolicy(t *testing.T) {
	h := unitHarness(t)
	h.run(ipAccessSteps(h), h.ipAccessDeleted())
}

func TestAccIPAccessPolicy(t *testing.T) {
	h := accHarness(t)
	h.run(ipAccessSteps(h), h.ipAccessDeleted())
}

func TestUnitIPAccessPolicyRefusesToOverwrite(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{
		{Config: h.config(h.projectConfig("ip"))},
		{
			PreConfig: func() {
				h.fake.with(func() {
					for id := range h.fake.projects {
						h.fake.x.ipAccess[formatID(id)] = &client.IPAccessPolicy{ProjectID: id, Configured: true, Mode: "deny",
							CIDRs: []client.IPAccessCIDR{{CIDR: "192.0.2.0/24"}}}
					}
				})
			},
			Config:      ipAccessConfig(h, ipDenyList, ""),
			ExpectError: regexp.MustCompile(`already has an IP access policy \(mode "deny", 1 CIDRs\)`),
		},
	}, nil)
}

func TestUnitIPAccessPolicyValidation(t *testing.T) {
	h := unitHarness(t)
	base := `resource "temps_ip_access_policy" "x" {
  project_id = 1
  %s
}`
	h.run(validationSteps(h, base, []struct{ body, want string }{
		{"mode = \"deny\"\n  cidrs = [{ cidr = \"10.1.2.3/8\" }]", `use the network address 10.0.0.0/8`},
		{"mode = \"deny\"\n  cidrs = [{ cidr = \"192.0.2.7\" }]", `stores it as 192.0.2.7/32`},
		{"mode = \"deny\"\n  cidrs = [{ cidr = \"2001:DB8::/32\" }]", `stores it as 2001:db8::/32`},
		{"mode = \"deny\"\n  cidrs = [{ cidr = \"::ffff:10.2.3.4/128\" }]", `stores it as 10.2.3.4/32`},
		{"mode = \"deny\"\n  cidrs = [{ cidr = \"10.0.0.0/33\" }]", `not an IPv4/IPv6 CIDR`},
		{"mode = \"deny\"\n  cidrs = [{ cidr = \"10.0.0.0/8\" }, { cidr = \"10.0.0.0/8\" }]", `already listed at index 0`},
		{"mode = \"allow\"", `would block every client`},
		{"mode = \"allow\"\n  cidrs = []", `would block every client`},
		{"mode = \"deny\"\n  cidrs = [{ cidr = \"10.0.0.0/8\", description = \"\" }]", `string length must be at least 1`},
	}), nil)
}

// ----------------------------------------------------------- cron monitor

const cronAddr = "temps_cron_monitor.report"

func cronConfig(h *harness, body string) string {
	return h.config(h.projectConfig("cron")+`
resource "temps_cron_monitor" "report" {
  project_id = temps_project.parent.id
  slug       = "nightly-report"
  %s
}`, body)
}

func cronSteps(h *harness) []resource.TestStep {
	return []resource.TestStep{
		{
			Config: cronConfig(h, `schedule       = { crontab = "0 3 * * *" }
  timezone       = "Europe/Madrid"
  checkin_margin = 5`),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(cronAddr, "max_runtime", "30"),
				resource.TestCheckResourceAttr(cronAddr, "enabled", "true"),
			),
		},
		{ResourceName: cronAddr, ImportState: true, ImportStateVerify: true, ImportStateIdFunc: stateID(cronAddr, "project_id", "id")},
		{
			// Temps resets omitted settings on PATCH; the provider sends them
			// all, so timezone and margin survive a schedule change.
			Config: cronConfig(h, `schedule       = { interval = 2, unit = "hour" }
  timezone       = "Europe/Madrid"
  checkin_margin = 5
  enabled        = false`),
			ConfigPlanChecks: expectAction(cronAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(cronAddr, "timezone", "Europe/Madrid"),
				resource.TestCheckResourceAttr(cronAddr, "checkin_margin", "5"),
				resource.TestCheckResourceAttr(cronAddr, "schedule.interval", "2"),
				resource.TestCheckResourceAttr(cronAddr, "status", "paused"),
			),
		},
	}
}

func (h *harness) cronsDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_cron_monitor", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetCronMonitor(context.Background(), mustID(rs.Primary.Attributes["project_id"]), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitCronMonitor(t *testing.T) {
	h := unitHarness(t)
	h.run(cronSteps(h), h.cronsDestroyed())
}

func TestAccCronMonitor(t *testing.T) {
	h := accHarness(t)
	h.run(cronSteps(h), h.cronsDestroyed())
}

func TestUnitCronMonitorValidation(t *testing.T) {
	h := unitHarness(t)
	base := `resource "temps_cron_monitor" "x" {
  project_id = 1
  slug       = "job"
  %s
}`
	h.run(validationSteps(h, base, []struct{ body, want string }{
		{`schedule = { crontab = "0 0 3 * * *" }`, `must have five fields`},
		{`schedule = { crontab = "0 3 * * *", interval = 1, unit = "hour" }`, `(?s)2 attributes specified when one \(and only one\)`},
		{`schedule = { interval = 1 }`, `"schedule.unit" must be specified`},
		{`schedule = { interval = 53, unit = "week" }`, `Temps accepts at most 525600`},
		{"schedule = { crontab = \"0 3 * * *\" }\n  timezone = \"Mars/Olympus\"", `not an IANA time zone`},
		{"schedule = { crontab = \"0 3 * * *\" }\n  checkin_margin = 0", `value must be between 1 and 40320`},
		{"schedule = { crontab = \"0 3 * * *\" }\n  failure_issue_threshold = 101", `value must be between 1 and 100`},
	}), nil)
	h.run([]resource.TestStep{{
		Config: h.config(`resource "temps_cron_monitor" "x" {
  project_id = 1
  slug       = "nightly report"
  schedule   = { crontab = "0 3 * * *" }
}`), PlanOnly: true, ExpectError: regexp.MustCompile(`letters, digits, '_' or '-'`),
	}}, nil)
}

// ------------------------------------------------------- deployment token

const tokenAddr = "temps_deployment_token.ci"

func tokenConfig(h *harness, body string) string {
	return h.config(h.projectConfig("token")+`
resource "temps_deployment_token" "ci" {
  project_id = temps_project.parent.id
  %s
}`, body)
}

func tokenSteps(h *harness) []resource.TestStep {
	perms := `permissions = ["emails:send", "analytics:read"]`
	expiry := `expires_at  = "2030-01-01T01:00:00+01:00"` // the server answers in UTC
	return []resource.TestStep{
		{
			Config:           tokenConfig(h, "name = \"ci\"\n  "+perms+"\n  "+expiry),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestMatchResourceAttr(tokenAddr, "token", regexp.MustCompile(`^dt_.{8,}`)),
				resource.TestCheckResourceAttr(tokenAddr, "permissions.#", "2"),
				resource.TestCheckResourceAttr(tokenAddr, "enabled", "true"),
			),
		},
		{ResourceName: tokenAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"token", "expires_at"},
			ImportStateIdFunc: stateID(tokenAddr, "project_id", "id")},
		{
			Config:           tokenConfig(h, "name = \"ci-renamed\"\n  enabled = false\n  "+perms+"\n  "+expiry),
			ConfigPlanChecks: expectAction(tokenAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(tokenAddr, "enabled", "false"),
				resource.TestMatchResourceAttr(tokenAddr, "token", regexp.MustCompile(`^dt_`)), // kept across updates
			),
		},
		{
			// The API cannot remove an expiry.
			Config:           tokenConfig(h, "name = \"ci-renamed\"\n  "+perms),
			ConfigPlanChecks: expectAction(tokenAddr, plancheck.ResourceActionReplace),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr(tokenAddr, "expires_at"),
				resource.TestCheckResourceAttr(tokenAddr, "enabled", "true"),
			),
		},
		{
			Config:      tokenConfig(h, "name = \"old\"\n  expires_at = \"2020-01-01T00:00:00Z\""),
			ExpectError: regexp.MustCompile(`expires_at "2020-01-01T00:00:00Z" is in the past`),
		},
	}
}

func (h *harness) tokensDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_deployment_token", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetDeploymentToken(context.Background(), mustID(rs.Primary.Attributes["project_id"]), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitDeploymentToken(t *testing.T) {
	h := unitHarness(t)
	h.run(tokenSteps(h), h.tokensDestroyed())
}

func TestAccDeploymentToken(t *testing.T) {
	h := accHarness(t)
	h.run(tokenSteps(h), h.tokensDestroyed())
}

// Temps itself accepts an environment of another project; the provider must not.
func tokenOtherProjectSteps(h *harness) []resource.TestStep {
	return []resource.TestStep{{
		Config: h.config(h.projectConfig("tok-a")+`
resource "temps_project" "other" {
  name        = %q
  source_type = "docker_image"
  preset      = "dockerfile"
}

data "temps_environment" "other_production" {
  project_id = temps_project.other.id
  name       = "production"
}

resource "temps_deployment_token" "cross" {
  project_id     = temps_project.parent.id
  environment_id = data.temps_environment.other_production.id
  name           = "cross"
}`, h.name("tok-b")),
		ExpectError: regexp.MustCompile(`is not an environment of project`),
	}}
}

func TestUnitDeploymentTokenOtherProjectEnvironment(t *testing.T) {
	h := unitHarness(t)
	h.run(tokenOtherProjectSteps(h), h.tokensDestroyed())
}

func TestAccDeploymentTokenOtherProjectEnvironment(t *testing.T) {
	h := accHarness(t)
	h.run(tokenOtherProjectSteps(h), h.tokensDestroyed())
}

// ------------------------------------------------------ team and access

const (
	teamAddr   = "temps_team.platform"
	accessAddr = "temps_project_access.platform"
)

func teamConfig(h *harness, slug, description, role string) string {
	return h.config(h.projectConfig("team")+`
resource "temps_team" "platform" {
  name        = "Platform"
  slug        = %q
  %s
}

resource "temps_project_access" "platform" {
  project_id = temps_project.parent.id
  team_id    = temps_team.platform.id
  role       = %q
}`, slug, description, role)
}

func teamSteps(h *harness) []resource.TestStep {
	slug := h.name("platform")
	return []resource.TestStep{
		{
			Config:           teamConfig(h, slug, `description = "Runs the platform"`, "deployer"),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check:            resource.TestCheckResourceAttr(accessAddr, "role", "deployer"),
		},
		{ResourceName: teamAddr, ImportState: true, ImportStateVerify: true},
		{ResourceName: accessAddr, ImportState: true, ImportStateVerify: true, ImportStateIdFunc: stateID(accessAddr, "project_id", "team_id")},
		{
			Config: teamConfig(h, slug, "", "viewer"),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(teamAddr, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(accessAddr, plancheck.ResourceActionUpdate),
				},
				PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
			},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr(teamAddr, "description"),
				resource.TestCheckResourceAttr(accessAddr, "role", "viewer"),
			),
		},
		{
			Config: teamConfig(h, slug+"-2", "", "viewer"),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(teamAddr, plancheck.ResourceActionReplace),
					plancheck.ExpectResourceAction(accessAddr, plancheck.ResourceActionReplace),
				},
				PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
			},
		},
	}
}

func (h *harness) teamsDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_team", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetTeam(context.Background(), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitTeam(t *testing.T) {
	h := unitHarness(t)
	h.run(teamSteps(h), h.teamsDestroyed())
}

func TestAccTeam(t *testing.T) {
	h := accHarness(t)
	h.run(teamSteps(h), h.teamsDestroyed())
}

func TestUnitProjectAccessRefusesExistingGrant(t *testing.T) {
	h := unitHarness(t)
	cfg := h.config(h.projectConfig("team") + `
resource "temps_team" "platform" {
  name = "Platform"
  slug = "platform"
}`)
	h.run([]resource.TestStep{
		{Config: cfg},
		{
			PreConfig: func() {
				h.fake.with(func() {
					for pid := range h.fake.projects {
						for tid := range h.fake.x.teams {
							h.fake.x.access[fmt.Sprintf("%d/%d", pid, tid)] = &client.ProjectAccess{ID: 999, ProjectID: pid, TeamID: tid, Role: "owner"}
						}
					}
				})
			},
			Config: cfg + `
resource "temps_project_access" "platform" {
  project_id = temps_project.parent.id
  team_id    = temps_team.platform.id
  role       = "viewer"
}`,
			ExpectError: regexp.MustCompile(`already has the "owner" role`),
		},
	}, nil)
}
