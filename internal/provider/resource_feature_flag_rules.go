// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

// Targeting rules of a feature flag in one environment
// (PUT /projects/{id}/flags/{key}/environments/{env}/rules).

import (
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/float64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var flagRuleOperators = []string{"eq", "neq", "in", "not_in", "contains", "not_contains", "matches", "gt", "gte", "lt", "lte", "exists", "not_exists"}

type flagRuleModel struct {
	Description       types.String         `tfsdk:"description"`
	Conditions        []flagConditionModel `tfsdk:"conditions"`
	RolloutPercentage types.Float64        `tfsdk:"rollout_percentage"`
	ServeValue        types.String         `tfsdk:"serve_value"`
	ServeVariant      types.String         `tfsdk:"serve_variant"`
	ServeSplit        []flagSplitModel     `tfsdk:"serve_split"`
}

type flagConditionModel struct {
	Attribute types.String `tfsdk:"attribute"`
	Operator  types.String `tfsdk:"operator"`
	Value     types.String `tfsdk:"value"`
}

type flagSplitModel struct {
	Variant types.String `tfsdk:"variant"`
	Weight  types.Int64  `tfsdk:"weight"`
}

func flagRulesAttribute() schema.Attribute {
	serveOthers := func(names ...string) []validator.String {
		var exprs []path.Expression
		for _, n := range names {
			exprs = append(exprs, path.MatchRelative().AtParent().AtName(n))
		}
		return []validator.String{stringvalidator.ExactlyOneOf(exprs...)}
	}
	return schema.ListNestedAttribute{
		Optional: true,
		MarkdownDescription: "Ordered targeting rules; the first rule whose conditions hold (and whose rollout includes the " +
			"subject) decides the value, after the kill switch and before `value`. Left out, Terraform does not manage the " +
			"rules and leaves them as they are; `rules = []` removes them all. Removing the argument stops managing them " +
			"without changing them. Destroying the resource removes rules it managed. Read back from " +
			"`GET /projects/{id}/flags/{key}` while set. Each rule serves exactly one of `serve_value`, `serve_variant` " +
			"and `serve_split`; variants must be declared on the flag.",
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Free text shown in the console, for example `Beta customers`."},
			"conditions": schema.ListNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Conditions that must all hold. Left out, the rule matches every subject.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"attribute": schema.StringAttribute{Required: true,
						MarkdownDescription: "Evaluation context attribute, for example `plan`, `country` or `email`."},
					"operator": schema.StringAttribute{Required: true,
						Validators:          []validator.String{stringvalidator.OneOf(flagRuleOperators...)},
						MarkdownDescription: "One of `" + joinTicks(flagRuleOperators) + "`."},
					"value": schema.StringAttribute{Optional: true, Validators: []validator.String{jsonString{}},
						MarkdownDescription: "JSON operand: a scalar such as `jsonencode(\"pro\")`, a list for `in` and `not_in` " +
							"(`jsonencode([\"de\", \"fr\"])`), a number for `gt`/`gte`/`lt`/`lte`. Leave it out for `exists` and `not_exists`."},
				}},
			},
			"rollout_percentage": schema.Float64Attribute{Optional: true,
				Validators:          []validator.Float64{float64validator.Between(0, 100)},
				MarkdownDescription: "Share of matching subjects the rule applies to (0–100); all of them when left out."},
			"serve_value": schema.StringAttribute{Optional: true,
				Validators:          append(serveOthers("serve_variant", "serve_split"), jsonString{}),
				MarkdownDescription: "Serve this JSON value, for example `jsonencode(true)`; it must match the flag's type."},
			"serve_variant": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Serve this variant of the flag."},
			"serve_split": schema.ListNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Split subjects across variants of the flag by weight; the weights add up to 100.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"variant": schema.StringAttribute{Required: true, MarkdownDescription: "Variant key."},
					"weight": schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.Between(0, 100)},
						MarkdownDescription: "Share of the rule's subjects, in whole percent."},
				}},
			},
		}},
	}
}

func flagRulesRequest(rules []flagRuleModel) []client.FlagRule {
	out := make([]client.FlagRule, len(rules))
	for i, r := range rules {
		conds := make([]client.FlagRuleCondition, len(r.Conditions))
		for j, c := range r.Conditions {
			conds[j] = client.FlagRuleCondition{Attribute: c.Attribute.ValueString(), Operator: c.Operator.ValueString()}
			if known(c.Value) {
				conds[j].Value = json.RawMessage(c.Value.ValueString())
			}
		}
		rule := client.FlagRule{Description: strPtr(r.Description), Conditions: conds}
		if known(r.RolloutPercentage) {
			rule.RolloutPercentage = ptr(r.RolloutPercentage.ValueFloat64())
		}
		switch {
		case known(r.ServeVariant):
			rule.Serve = client.FlagRuleServe{Kind: "variant", Variant: r.ServeVariant.ValueString()}
		case r.ServeSplit != nil:
			split := make([]client.FlagVariantWeight, len(r.ServeSplit))
			for j, s := range r.ServeSplit {
				split[j] = client.FlagVariantWeight{Variant: s.Variant.ValueString(), Weight: s.Weight.ValueInt64()}
			}
			rule.Serve = client.FlagRuleServe{Kind: "split", Split: split}
		default:
			rule.Serve = client.FlagRuleServe{Kind: "value", Value: json.RawMessage(r.ServeValue.ValueString())}
		}
		out[i] = rule
	}
	return out
}

// flagRulesFromAPI converts the server's rules, keeping the configured
// spelling of JSON values and of an empty list of conditions (prior holds
// the rules from configuration or state, matched by position).
func flagRulesFromAPI(rules []client.FlagRule, prior []flagRuleModel) ([]flagRuleModel, error) {
	out := make([]flagRuleModel, len(rules))
	for i, r := range rules {
		var p flagRuleModel
		if i < len(prior) {
			p = prior[i]
		}
		m := flagRuleModel{
			Description: types.StringPointerValue(r.Description), RolloutPercentage: types.Float64PointerValue(r.RolloutPercentage),
			ServeValue: types.StringNull(), ServeVariant: types.StringNull(),
		}
		if len(r.Conditions) > 0 || p.Conditions != nil {
			m.Conditions = make([]flagConditionModel, len(r.Conditions))
		}
		for j, c := range r.Conditions {
			var pc flagConditionModel
			if j < len(p.Conditions) {
				pc = p.Conditions[j]
			}
			m.Conditions[j] = flagConditionModel{Attribute: types.StringValue(c.Attribute), Operator: types.StringValue(c.Operator),
				Value: jsonValue(pc.Value, c.Value)}
		}
		switch r.Serve.Kind {
		case "value":
			m.ServeValue = jsonValue(p.ServeValue, r.Serve.Value)
			if m.ServeValue.IsNull() {
				m.ServeValue = types.StringValue("null")
			}
		case "variant":
			m.ServeVariant = types.StringValue(r.Serve.Variant)
		case "split":
			m.ServeSplit = make([]flagSplitModel, len(r.Serve.Split))
			for j, s := range r.Serve.Split {
				m.ServeSplit[j] = flagSplitModel{Variant: types.StringValue(s.Variant), Weight: types.Int64Value(s.Weight)}
			}
		default:
			return nil, fmt.Errorf("rule %d serves %q, which this provider does not know; upgrade the provider", i, r.Serve.Kind)
		}
		out[i] = m
	}
	return out, nil
}
