// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

// stateInt64 reads an integer attribute of a resource in state.
func stateInt64(s *terraform.State, addr, attr string) (int64, error) {
	rs, ok := s.RootModule().Resources[addr]
	if !ok {
		return 0, fmt.Errorf("%s not in state", addr)
	}
	return parseID(rs.Primary.Attributes[attr])
}

// ----------------------------------------------------------- project secret

const secretAddr = "temps_project_secret.token"

func secretConfig(h *harness, value, envs string) string {
	return h.config(h.projectConfig("secret") + fmt.Sprintf(`
resource "temps_project_secret" "token" {
  project_id      = temps_project.parent.id
  key             = "API_TOKEN"
  value           = %q
  environment_ids = %s
}`, value, envs))
}

// secretScope checks the server-side scope of the secret, which the value
// rotation must not widen.
func (h *harness) secretScope(want int) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		pid, err := stateInt64(s, secretAddr, "project_id")
		if err != nil {
			return err
		}
		id, err := stateInt64(s, secretAddr, "id")
		if err != nil {
			return err
		}
		sec, err := h.client.GetProjectSecret(context.Background(), pid, id)
		if err != nil {
			return err
		}
		if got := len(sec.EnvironmentIDs()); got != want {
			return fmt.Errorf("secret applies to %d environments on the server, want %d", got, want)
		}
		return nil
	}
}

func secretSteps(h *harness) []resource.TestStep {
	prod := "[data.temps_environment.production.id]"
	return []resource.TestStep{
		{
			Config:           secretConfig(h, "one", prod),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(secretAddr, "environment_ids.#", "1"),
				resource.TestCheckResourceAttr(secretAddr, "include_in_preview", "false"),
				h.secretScope(1),
			),
		},
		// Rotating only the value keeps the scope: the provider resends it.
		{
			Config:           secretConfig(h, "two", prod),
			ConfigPlanChecks: expectAction(secretAddr, plancheck.ResourceActionUpdate),
			Check:            h.secretScope(1),
		},
		// An empty scope means every environment.
		{
			Config:           secretConfig(h, "two", "[]"),
			ConfigPlanChecks: expectAction(secretAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(secretAddr, "environment_ids.#", "0"),
				h.secretScope(0),
			),
		},
		{
			ResourceName: secretAddr, ImportState: true, ImportStateVerify: true,
			ImportStateIdFunc: stateID(secretAddr, "project_id", "id"), ImportStateVerifyIgnore: []string{"value"},
		},
	}
}

func (h *harness) secretsDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_project_secret", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetProjectSecret(context.Background(), mustID(rs.Primary.Attributes["project_id"]), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitProjectSecret(t *testing.T) {
	h := unitHarness(t)
	h.run(secretSteps(h), h.secretsDestroyed())
}

// Released servers send the secret list as one bare array.
func TestUnitProjectSecretLegacyList(t *testing.T) {
	h := unitHarness(t)
	h.fake.legacyShapes = true
	h.run(secretSteps(h), h.secretsDestroyed())
}

// A secret pushed onto a later page by newer ones is still found: the plan
// stays empty instead of creating it again.
func TestUnitProjectSecretOnALaterPage(t *testing.T) {
	h := unitHarness(t)
	prod := "[data.temps_environment.production.id]"
	h.run([]resource.TestStep{
		{
			Config:           secretConfig(h, "one", prod),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: func(s *terraform.State) error {
				pid, err := stateInt64(s, secretAddr, "project_id")
				if err != nil {
					return err
				}
				// 250 newer secrets: pages of 100 put this one on page 3.
				h.fake.with(func() {
					for i := range 250 {
						id := h.fake.nextID()
						h.fake.al.secrets[id] = &fakeSecret{s: client.ProjectSecret{ID: id, ProjectID: pid, Key: fmt.Sprintf("NEWER_%d", i)}}
					}
				})
				return nil
			},
		},
		// Were it missed, the refresh would drop it and the plan create it.
		{Config: secretConfig(h, "one", prod), PlanOnly: true},
	}, h.secretsDestroyed())
}

func TestAccProjectSecret(t *testing.T) {
	h := accHarness(t)
	h.run(secretSteps(h), h.secretsDestroyed())
}

func TestUnitProjectSecretRejectsBadKey(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: h.config(`
resource "temps_project_secret" "bad" {
  project_id = 1
  key        = "1BAD KEY"
  value      = "x"
}`),
		ExpectError: regexp.MustCompile(`must start with a letter`),
	}}, nil)
}

// ------------------------------------------------------- notification route

const routeAddr = "temps_notification_route.critical"

func routeConfig(h *harness, min, max string) string {
	return h.config(`
resource "temps_notification_provider" "pager" {
  name = %q
  url  = "https://hooks.example.com/pager"
}

resource "temps_notification_route" "critical" {
  name         = %q
  min_severity = %q
  max_severity = %q
  provider_ids = [temps_notification_provider.pager.id]
}`, h.name("pager"), h.name("critical"), min, max)
}

func routeSteps(h *harness) []resource.TestStep {
	return []resource.TestStep{
		{
			Config:           routeConfig(h, "error", "emergency"),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(routeAddr, "enabled", "true"),
				resource.TestCheckResourceAttr(routeAddr, "provider_ids.#", "1"),
			),
		},
		{
			Config:           routeConfig(h, "critical", "critical"),
			ConfigPlanChecks: expectAction(routeAddr, plancheck.ResourceActionUpdate),
			Check:            resource.TestCheckResourceAttr(routeAddr, "min_severity", "critical"),
		},
		// The provider is deleted outside Terraform: Temps keeps the route
		// with no providers, and the next apply recreates the provider and
		// points the route at it again.
		{
			PreConfig: func() {
				for _, p := range h.notifierIDs() {
					if err := h.client.DeleteNotificationProvider(context.Background(), p); err != nil {
						h.t.Fatal(err)
					}
				}
			},
			Config:           routeConfig(h, "critical", "critical"),
			ConfigPlanChecks: expectAction(routeAddr, plancheck.ResourceActionUpdate),
			Check:            resource.TestCheckResourceAttr(routeAddr, "provider_ids.#", "1"),
		},
		{ResourceName: routeAddr, ImportState: true, ImportStateVerify: true},
	}
}

// notifierIDs lists the notification providers the last applied step created.
func (h *harness) notifierIDs() []int64 { return h.lastNotifiers }

func (h *harness) routesDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_notification_route", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetNotificationRoute(context.Background(), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitNotificationRoute(t *testing.T) {
	h := unitHarness(t)
	h.run(h.trackNotifiers(routeSteps(h)), h.routesDestroyed())
}

func TestAccNotificationRoute(t *testing.T) {
	h := accHarness(t)
	h.run(h.trackNotifiers(routeSteps(h)), h.routesDestroyed())
}

// trackNotifiers records the provider ID after each step so a later step can
// delete it out of band.
func (h *harness) trackNotifiers(steps []resource.TestStep) []resource.TestStep {
	for i := range steps {
		if steps[i].ImportState {
			continue
		}
		check := steps[i].Check
		steps[i].Check = func(s *terraform.State) error {
			if id, err := stateInt64(s, "temps_notification_provider.pager", "id"); err == nil {
				h.lastNotifiers = []int64{id}
			}
			if check != nil {
				return check(s)
			}
			return nil
		}
	}
	return steps
}

func TestUnitNotificationRouteRejectsInvertedRange(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: h.config(`
resource "temps_notification_route" "bad" {
  name         = "inverted"
  min_severity = "critical"
  max_severity = "info"
  provider_ids = [1]
}`),
		ExpectError: regexp.MustCompile(`higher than max_severity`),
	}}, nil)
}

// --------------------------------------------------------- error alert rule

const alertAddr = "temps_error_alert_rule.burst"

func alertConfig(h *harness, cooldown int, errorType string) string {
	et := ""
	if errorType != "" {
		et = fmt.Sprintf("error_type = %q", errorType)
	}
	return h.config(h.projectConfig("alerts") + fmt.Sprintf(`
resource "temps_error_alert_rule" "burst" {
  project_id       = temps_project.parent.id
  name             = "burst"
  trigger_type     = "frequency"
  trigger_config   = jsonencode({ window_minutes = 60, count = 100 })
  environment_id   = data.temps_environment.production.id
  priority         = "Critical"
  cooldown_minutes = %d
  %s
}`, cooldown, et))
}

func alertSteps(h *harness) []resource.TestStep {
	return []resource.TestStep{
		{
			Config:           alertConfig(h, 30, "TypeError"),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(alertAddr, "error_type", "TypeError"),
				resource.TestCheckResourceAttr(alertAddr, "enabled", "true"),
			),
		},
		{
			Config:           alertConfig(h, 5, "TypeError"),
			ConfigPlanChecks: expectAction(alertAddr, plancheck.ResourceActionUpdate),
			Check:            resource.TestCheckResourceAttr(alertAddr, "cooldown_minutes", "5"),
		},
		// Temps cannot remove the error type filter, so the rule is replaced.
		{
			Config:           alertConfig(h, 5, ""),
			ConfigPlanChecks: expectAction(alertAddr, plancheck.ResourceActionDestroyBeforeCreate),
			Check:            resource.TestCheckNoResourceAttr(alertAddr, "error_type"),
		},
		{
			ResourceName: alertAddr, ImportState: true, ImportStateVerify: true,
			ImportStateIdFunc: stateID(alertAddr, "project_id", "id"),
		},
	}
}

func (h *harness) alertRulesDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_error_alert_rule", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetErrorAlertRule(context.Background(), mustID(rs.Primary.Attributes["project_id"]), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitErrorAlertRule(t *testing.T) {
	h := unitHarness(t)
	h.run(alertSteps(h), h.alertRulesDestroyed())
}

func TestAccErrorAlertRule(t *testing.T) {
	h := accHarness(t)
	h.run(alertSteps(h), h.alertRulesDestroyed())
}

func TestUnitErrorAlertRuleNeedsTriggerConfig(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: h.config(`
resource "temps_error_alert_rule" "bad" {
  project_id   = 1
  name         = "burst"
  trigger_type = "frequency"
}`),
		ExpectError: regexp.MustCompile(`trigger_config.count`),
	}}, nil)
}

// ---------------------------------------------------------------- edge rule

const edgeAddr = "temps_edge_rule.admin"

func edgeConfig(h *harness, extra string) string {
	return h.config(h.projectConfig("edge") + fmt.Sprintf(`
resource "temps_edge_rule" "admin" {
  project_id = temps_project.parent.id
  name       = "admin"
  expression = jsonencode({
    combinator = "all"
    children = [{ kind = "condition", field = "path", op = "starts_with", values = ["/admin"] }]
  })
  %s
}`, extra))
}

func edgeSteps(h *harness) []resource.TestStep {
	return []resource.TestStep{
		{
			Config:           edgeConfig(h, `action = "block"`),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(edgeAddr, "mode", "log_only"),
				resource.TestCheckNoResourceAttr(edgeAddr, "status"),
				resource.TestCheckResourceAttr(edgeAddr, "revision", "1"),
			),
		},
		{
			Config:           edgeConfig(h, "action = \"block\"\n  status = 404\n  mode = \"enforce\"\n  body = \"gone\""),
			ConfigPlanChecks: expectAction(edgeAddr, plancheck.ResourceActionUpdate),
			Check:            resource.TestCheckResourceAttr(edgeAddr, "status", "404"),
		},
		// The rule is renamed outside Terraform: the refresh picks up its new
		// revision, so the update reverting the name is not refused.
		{
			PreConfig:        func() { h.renameEdgeRules("renamed-outside") },
			Config:           edgeConfig(h, "action = \"redirect\"\n  redirect_target = \"/login?next={path}\"\n  mode = \"enforce\""),
			ConfigPlanChecks: expectAction(edgeAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(edgeAddr, "name", "admin"),
				resource.TestCheckNoResourceAttr(edgeAddr, "status"),
				resource.TestCheckNoResourceAttr(edgeAddr, "body"),
			),
		},
		{
			ResourceName: edgeAddr, ImportState: true, ImportStateVerify: true,
			ImportStateIdFunc: stateID(edgeAddr, "project_id", "id"),
			// The configured spelling of the expression is kept in state; an
			// import reads the server's canonical one.
			ImportStateVerifyIgnore: []string{"expression"},
		},
	}
}

// renameEdgeRules renames every edge rule this harness created, out of band.
func (h *harness) renameEdgeRules(name string) {
	for _, id := range h.lastEdges {
		ctx := context.Background()
		e, err := h.client.GetEdgeRule(ctx, id[0], id[1])
		if err != nil {
			h.t.Fatal(err)
		}
		_, err = h.client.UpdateEdgeRule(ctx, id[0], id[1], client.EdgeRuleRequest{
			Name: name, EnvironmentID: e.EnvironmentID, Enabled: e.Enabled, Mode: e.Mode, Expression: e.Expression, Action: e.Action,
		})
		if err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *harness) trackEdges(steps []resource.TestStep) []resource.TestStep {
	for i := range steps {
		if steps[i].ImportState {
			continue
		}
		check := steps[i].Check
		steps[i].Check = func(s *terraform.State) error {
			pid, err1 := stateInt64(s, edgeAddr, "project_id")
			id, err2 := stateInt64(s, edgeAddr, "id")
			if err1 == nil && err2 == nil {
				h.lastEdges = [][2]int64{{pid, id}}
			}
			if check != nil {
				return check(s)
			}
			return nil
		}
	}
	return steps
}

func (h *harness) edgesDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_edge_rule", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetEdgeRule(context.Background(), mustID(rs.Primary.Attributes["project_id"]), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitEdgeRule(t *testing.T) {
	h := unitHarness(t)
	h.run(h.trackEdges(edgeSteps(h)), h.edgesDestroyed())
}

func TestAccEdgeRule(t *testing.T) {
	h := accHarness(t)
	h.run(h.trackEdges(edgeSteps(h)), h.edgesDestroyed())
}

func TestUnitEdgeRuleRejectsEmptyGroupAndMissingTarget(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{
		{
			Config: h.config(`
resource "temps_edge_rule" "bad" {
  project_id = 1
  name       = "everything"
  action     = "block"
  expression = jsonencode({ combinator = "none", children = [] })
}`),
			ExpectError: regexp.MustCompile(`matches every request`),
		},
		{
			Config: h.config(`
resource "temps_edge_rule" "bad" {
  project_id = 1
  name       = "redirect"
  action     = "redirect"
  expression = jsonencode({ combinator = "all", children = [{ kind = "condition", field = "path", op = "eq", values = ["/"] }] })
}`),
			ExpectError: regexp.MustCompile(`needs redirect_target`),
		},
	}, nil)
}

func TestExpressionEqualIgnoresServerAdditions(t *testing.T) {
	cfg := `{"combinator":"all","children":[{"kind":"condition","field":"path","op":"exists"}]}`
	srv := `{"id":"n1","combinator":"all","children":[{"kind":"condition","id":"n2","field":"path","op":"exists","values":[],"case_insensitive":false,"key":null}]}`
	if !expressionEqual(cfg, srv) {
		t.Fatal("server-added ids and defaults should not count as a difference")
	}
	changed := `{"combinator":"all","children":[{"kind":"condition","field":"path","op":"exists","case_insensitive":true}]}`
	if expressionEqual(changed, srv) {
		t.Fatal("case_insensitive=true is a real difference")
	}
	var v any
	if err := json.Unmarshal([]byte(srv), &v); err != nil {
		t.Fatal(err)
	}
}
