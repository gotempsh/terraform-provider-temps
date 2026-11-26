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

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

func flagRulesConfig(h *harness, rules string) string {
	return flagConfig(h, "bool", `jsonencode(false)`, fmt.Sprintf(flagEnvBlock, "value = jsonencode(true)\n"+rules))
}

const twoRules = `
  rules = [
    {
      description        = "Paying customers"
      conditions         = [{ attribute = "plan", operator = "in", value = jsonencode(["pro", "team"]) }]
      rollout_percentage = 50
      serve_value        = jsonencode(true)
    },
    {
      conditions  = [{ attribute = "country", operator = "exists" }]
      serve_value = jsonencode(false)
    },
  ]`

const oneRule = `
  rules = [
    {
      conditions = [
        { attribute = "plan", operator = "eq", value = jsonencode("pro") },
        { attribute = "seats", operator = "gte", value = jsonencode(10) },
      ]
      serve_split = [{ variant = "on", weight = 70 }, { variant = "off", weight = 30 }]
    },
  ]`

// flagEnvRules returns the rules the fake stores for the only flag environment.
func (h *harness) flagEnvRules() []client.FlagRule {
	var out []client.FlagRule
	h.fake.with(func() {
		for _, fl := range h.fake.x.flags {
			for _, e := range fl.Environments {
				if e.Rules != nil {
					out = *e.Rules
				}
			}
		}
	})
	return out
}

func (h *harness) wantRules(n int) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := len(h.flagEnvRules()); got != n {
			return fmt.Errorf("server has %d rules, want %d", got, n)
		}
		return nil
	}
}

func (h *harness) setFlagRules(rules []client.FlagRule) func() {
	return func() {
		h.fake.with(func() {
			for _, fl := range h.fake.x.flags {
				for i := range fl.Environments {
					r := rules
					fl.Environments[i].Rules = &r
					h.fake.st.touchFlagEnv(&fl.Environments[i])
				}
			}
		})
	}
}

func (h *harness) ruleSets() int {
	var n int
	h.fake.with(func() { n = h.fake.st.ruleSets })
	return n
}

func TestUnitFeatureFlagRules(t *testing.T) {
	h := unitHarness(t)
	var sets int
	h.run([]resource.TestStep{
		{
			Config:           flagRulesConfig(h, twoRules),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(flagEnvAddr, "rules.#", "2"),
				resource.TestCheckResourceAttr(flagEnvAddr, "rules.0.description", "Paying customers"),
				resource.TestCheckResourceAttr(flagEnvAddr, "rules.0.conditions.0.value", `["pro","team"]`),
				resource.TestCheckResourceAttr(flagEnvAddr, "rules.0.rollout_percentage", "50"),
				resource.TestCheckNoResourceAttr(flagEnvAddr, "rules.1.conditions.0.value"),
				resource.TestCheckNoResourceAttr(flagEnvAddr, "rules.1.rollout_percentage"),
				resource.TestCheckResourceAttr(flagEnvAddr, "rules.1.serve_value", "false"),
				h.wantRules(2),
				func(*terraform.State) error {
					r := h.flagEnvRules()
					if r[0].Serve.Kind != "value" || string(r[0].Serve.Value) != "true" || r[0].RolloutPercentage == nil || *r[0].RolloutPercentage != 50 {
						return fmt.Errorf("first rule stored as %+v", r[0])
					}
					if r[1].Conditions[0].Operator != "exists" || r[1].Conditions[0].Value != nil {
						return fmt.Errorf("second rule stored as %+v", r[1])
					}
					sets = h.ruleSets()
					return nil
				},
			),
		},
		{
			ResourceName: flagEnvAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"value"},
			ImportStateIdFunc: stateID(flagEnvAddr, "project_id", "key", "environment_id"),
		},
		{
			Config:           flagRulesConfig(h, oneRule),
			ConfigPlanChecks: expectAction(flagEnvAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(flagEnvAddr, "rules.#", "1"),
				resource.TestCheckNoResourceAttr(flagEnvAddr, "rules.0.description"),
				resource.TestCheckNoResourceAttr(flagEnvAddr, "rules.0.serve_value"),
				resource.TestCheckResourceAttr(flagEnvAddr, "rules.0.conditions.1.value", "10"),
				resource.TestCheckResourceAttr(flagEnvAddr, "rules.0.serve_split.0.weight", "70"),
				h.wantRules(1),
			),
		},
		{
			// A rule added in the console is detected and removed again.
			PreConfig: func() {
				r := h.flagEnvRules()
				r = append(r, client.FlagRule{Conditions: []client.FlagRuleCondition{}, Serve: client.FlagRuleServe{Kind: "value", Value: []byte("true")}})
				h.setFlagRules(r)()
			},
			Config:           flagRulesConfig(h, oneRule),
			ConfigPlanChecks: expectAction(flagEnvAddr, plancheck.ResourceActionUpdate),
			Check:            h.wantRules(1),
		},
		{
			// Someone saves the rules between the read and the write: Temps
			// refuses instead of overwriting.
			PreConfig:   func() { h.fake.with(func() { h.fake.st.bumpRules = true }) },
			Config:      flagRulesConfig(h, twoRules),
			ExpectError: regexp.MustCompile(`(?s)Unable to set the targeting rules.*409.*changed since\s+they were read`),
		},
		{
			Config:           flagRulesConfig(h, twoRules),
			ConfigPlanChecks: expectAction(flagEnvAddr, plancheck.ResourceActionUpdate),
			Check:            h.wantRules(2),
		},
		{
			Config:           flagRulesConfig(h, "  rules = []"),
			ConfigPlanChecks: expectAction(flagEnvAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(flagEnvAddr, "rules.#", "0"),
				h.wantRules(0),
			),
		},
		{
			// Removing the argument stops managing the rules: the ones made in
			// the console stay, and later console changes plan nothing.
			PreConfig: h.setFlagRules([]client.FlagRule{{Conditions: []client.FlagRuleCondition{},
				Serve: client.FlagRuleServe{Kind: "value", Value: []byte("false")}}}),
			Config: flagRulesConfig(h, ""),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(flagEnvAddr, plancheck.ResourceActionUpdate)},
				PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
			},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr(flagEnvAddr, "rules.#"),
				h.wantRules(1),
				func(*terraform.State) error {
					// Since step 1, steps 3, 4, 6 and 7 wrote rules; step 5 was refused.
					if got := h.ruleSets() - sets; got != 4 {
						return fmt.Errorf("%d rule writes since the first apply, want 4", got)
					}
					return nil
				},
			),
		},
		{
			PreConfig:        h.setFlagRules(nil),
			Config:           flagRulesConfig(h, ""),
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
		},
	}, nil)
}

// Destroying the resource removes the rules it managed; rules it never
// managed are left alone.
func TestUnitFeatureFlagRulesDestroy(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{
		{Config: flagRulesConfig(h, twoRules), Check: h.wantRules(2)},
		{Config: flagConfig(h, "bool", `jsonencode(false)`, ""), Check: h.wantRules(0)},
		{
			Config:    flagRulesConfig(h, ""),
			PreConfig: h.setFlagRules([]client.FlagRule{{Conditions: []client.FlagRuleCondition{}, Serve: client.FlagRuleServe{Kind: "value", Value: []byte("true")}}}),
			Check:     h.wantRules(1),
		},
		{Config: flagConfig(h, "bool", `jsonencode(false)`, ""), Check: h.wantRules(1)},
	}, nil)
}

func TestUnitFeatureFlagRulesErrors(t *testing.T) {
	t.Run("validation", func(t *testing.T) {
		h := unitHarness(t)
		for _, tc := range []struct{ rules, want string }{
			{`  rules = [{ description = "no serve" }]`, `(?s)No attribute specified when one \(and only one\) of`},
			{`  rules = [{ serve_value = "true", serve_variant = "on" }]`, `(?s)2 attributes specified when one \(and only one\)`},
			{`  rules = [{ serve_value = "yes" }]`, `must be a JSON document`},
			{`  rules = [{ serve_value = "true", rollout_percentage = 101 }]`, `between 0.000000 and\s+100.000000`},
			{`  rules = [{ serve_split = [{ variant = "a", weight = 101 }] }]`, `between 0 and 100`},
			{`  rules = [{ serve_value = "true", conditions = [{ attribute = "plan", operator = "like" }] }]`, `value must be one of`},
			{`  rules = [{ serve_value = "true", conditions = [{ attribute = "plan", operator = "eq", value = "pro" }] }]`, `must be a JSON document`},
		} {
			h.run([]resource.TestStep{{Config: flagRulesConfig(h, tc.rules), PlanOnly: true,
				ExpectError: regexp.MustCompile(tc.want)}}, nil)
		}
	})
	t.Run("refused by the server", func(t *testing.T) {
		h := unitHarness(t)
		h.run([]resource.TestStep{{
			Config:      flagRulesConfig(h, `  rules = [{ serve_split = [{ variant = "on", weight = 60 }] }]`),
			ExpectError: regexp.MustCompile(`(?s)Unable to set the targeting rules.*add up to 60, not 100`),
		}}, nil)
	})
	t.Run("old server", func(t *testing.T) {
		h := unitHarness(t)
		h.fake.st.old = true
		h.run([]resource.TestStep{
			{Config: flagRulesConfig(h, ""), ConfigPlanChecks: emptyPlanAfterApply()},
			{
				Config:      flagRulesConfig(h, twoRules),
				ExpectError: regexp.MustCompile(`(?s)targeting rules.*does not provide this endpoint`),
			},
		}, nil)
	})
}
