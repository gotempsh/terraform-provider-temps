// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var edgeRulePermissions = []string{"edge_rules:read", "edge_rules:write"}

var edgeRuleActions = []string{"block", "allow", "challenge", "redirect", "log"}

// edgeDefaultStatus is the status the provider sends when none is configured.
var edgeDefaultStatus = map[string]int64{"block": 403, "redirect": 302}

var (
	_ resource.Resource                   = &edgeRuleResource{}
	_ resource.ResourceWithImportState    = &edgeRuleResource{}
	_ resource.ResourceWithValidateConfig = &edgeRuleResource{}
)

type edgeRuleResource struct{ resourceBase }

func newEdgeRuleResource() resource.Resource { return &edgeRuleResource{} }

type edgeRuleModel struct {
	ID             types.String `tfsdk:"id"`
	ProjectID      types.Int64  `tfsdk:"project_id"`
	EnvironmentID  types.Int64  `tfsdk:"environment_id"`
	Name           types.String `tfsdk:"name"`
	Enabled        types.Bool   `tfsdk:"enabled"`
	Mode           types.String `tfsdk:"mode"`
	Expression     types.String `tfsdk:"expression"`
	Action         types.String `tfsdk:"action"`
	Status         types.Int64  `tfsdk:"status"`
	RedirectTarget types.String `tfsdk:"redirect_target"`
	Body           types.String `tfsdk:"body"`
	Position       types.Int64  `tfsdk:"position"`
	Revision       types.Int64  `tfsdk:"revision"`
	Summary        types.String `tfsdk:"summary"`
}

func (r *edgeRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_edge_rule"
}

func (r *edgeRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A request filter the Temps proxy evaluates before a project's application: block, allow, " +
			"challenge, redirect or log requests that match an expression.\n\n" +
			"New rules start in `log_only` mode, which records matches without acting; set `mode = \"enforce\"` once the " +
			"rule matches what you expect. Rules are evaluated in `position` order and new rules go last.\n\n" +
			"An expression group with no children matches every request, so the provider refuses one.\n\n" +
			"Import with `<project_id>/<rule_id>`.\n\n" +
			"Required API key permissions: " + permList(edgeRulePermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric rule ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the project. Changing it replaces the rule.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"environment_id": schema.Int64Attribute{Optional: true,
				MarkdownDescription: "Apply the rule to this environment only. Unset applies it to every environment of the project."},
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Rule name, up to 80 characters.",
				Validators: []validator.String{notBlank{}, stringvalidator.LengthAtMost(80)}},
			"enabled": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "Whether the rule is evaluated. Defaults to `true`."},
			"mode": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("log_only"),
				MarkdownDescription: "`log_only` (the default) records matches without acting; `enforce` acts on them.",
				Validators:          []validator.String{stringvalidator.OneOf("log_only", "enforce")}},
			"expression": schema.StringAttribute{Required: true,
				MarkdownDescription: "Match expression as JSON: a group `{ combinator, children }` where `combinator` is " +
					"`all`, `any` or `none` and each child is a condition " +
					"(`{ kind = \"condition\", field, op, values, key, case_insensitive }`) or a nested group " +
					"(`{ kind = \"group\", combinator, children }`), at most 4 levels deep. Use `jsonencode()`.",
				Validators: []validator.String{jsonString{}}},
			"action": schema.StringAttribute{Required: true,
				MarkdownDescription: "What a match does: one of `" + joinTicks(edgeRuleActions) + "`.",
				Validators:          []validator.String{stringvalidator.OneOf(edgeRuleActions...)}},
			"status": schema.Int64Attribute{Optional: true,
				MarkdownDescription: "Response status: 400-499 for `block` (default `403`), 301, 302, 307 or 308 for `redirect` (default `302`)."},
			"redirect_target": schema.StringAttribute{Optional: true,
				MarkdownDescription: "For `redirect`: a path starting with `/` or an absolute http(s) URL. " +
					"`{path}` and `{query}` insert the request's path and query string."},
			"body": schema.StringAttribute{Optional: true,
				MarkdownDescription: "For `block`: plain-text response body, up to 1024 bytes.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 1024)}},
			"position": schema.Int64Attribute{Computed: true, MarkdownDescription: "0-based evaluation order.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
			"revision": schema.Int64Attribute{Computed: true,
				MarkdownDescription: "Edit counter. Updates send it, so a rule changed since the last refresh is refused instead of overwritten."},
			"summary": schema.StringAttribute{Computed: true, MarkdownDescription: "The rule in one sentence, as Temps describes it."},
		},
	}
}

func (r *edgeRuleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg edgeRuleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if known(cfg.Expression) {
		var root map[string]any
		if json.Unmarshal([]byte(cfg.Expression.ValueString()), &root) != nil {
			resp.Diagnostics.AddAttributeError(path.Root("expression"), "Invalid expression", "expression must be a JSON object (a group).")
		} else if where := emptyGroup(root, "expression"); where != "" {
			resp.Diagnostics.AddAttributeError(path.Root("expression"), "Empty expression group",
				where+" has no children. An empty group matches every request, so this rule would apply to all traffic; add a condition.")
		}
	}
	if !known(cfg.Action) {
		return
	}
	action := cfg.Action.ValueString()
	bad := func(attr, why string) {
		resp.Diagnostics.AddAttributeError(path.Root(attr), "Invalid "+attr+" for action "+action, why)
	}
	status := cfg.Status
	switch action {
	case "block":
		if known(status) && (status.ValueInt64() < 400 || status.ValueInt64() > 499) {
			bad("status", fmt.Sprintf("A block status must be 400-499, got %d.", status.ValueInt64()))
		}
	case "redirect":
		if cfg.RedirectTarget.IsNull() {
			bad("redirect_target", "A redirect needs redirect_target.")
		}
		if known(status) && !slices.Contains([]int64{301, 302, 307, 308}, status.ValueInt64()) {
			bad("status", fmt.Sprintf("A redirect status must be 301, 302, 307 or 308, got %d.", status.ValueInt64()))
		}
	default:
		if !status.IsNull() {
			bad("status", "Only block and redirect take a status.")
		}
	}
	if action != "redirect" && !cfg.RedirectTarget.IsNull() {
		bad("redirect_target", "Only redirect takes redirect_target.")
	}
	if action != "block" && !cfg.Body.IsNull() {
		bad("body", "Only block takes a body.")
	}
}

// emptyGroup returns where the first group without children is, or "".
func emptyGroup(group map[string]any, where string) string {
	children, _ := group["children"].([]any)
	if len(children) == 0 {
		return where
	}
	for i, c := range children {
		child, ok := c.(map[string]any)
		if ok && child["kind"] == "group" {
			if w := emptyGroup(child, fmt.Sprintf("%s.children[%d]", where, i)); w != "" {
				return w
			}
		}
	}
	return ""
}

// canonicalExpression drops what the server adds to an expression (node ids,
// case_insensitive=false, empty values) so equal rules compare equal.
func canonicalExpression(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range t {
			if k == "id" {
				continue
			}
			if k == "case_insensitive" && val == false {
				continue
			}
			if k == "key" && val == nil {
				continue
			}
			if arr, ok := val.([]any); ok && len(arr) == 0 && k == "values" {
				continue
			}
			out[k] = canonicalExpression(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = canonicalExpression(e)
		}
		return out
	default:
		return v
	}
}

func expressionEqual(a, b string) bool {
	var va, vb any
	if json.Unmarshal([]byte(a), &va) != nil || json.Unmarshal([]byte(b), &vb) != nil {
		return false
	}
	return reflect.DeepEqual(canonicalExpression(va), canonicalExpression(vb))
}

func (m *edgeRuleModel) fromAPI(r *client.EdgeRule) {
	m.ID = types.StringValue(formatID(r.ID))
	m.ProjectID = types.Int64Value(r.ProjectID)
	m.EnvironmentID = types.Int64PointerValue(r.EnvironmentID)
	m.Name = types.StringValue(r.Name)
	m.Enabled = types.BoolValue(r.Enabled)
	m.Mode = types.StringValue(r.Mode)
	if !known(m.Expression) || !expressionEqual(m.Expression.ValueString(), string(r.Expression)) {
		canon, _ := json.Marshal(canonicalExpression(mustUnmarshal(r.Expression)))
		m.Expression = types.StringValue(string(canon))
	}
	m.Action = types.StringValue(r.Action.Type)
	// Unset status means the action's default; keep it unset when the server
	// reports exactly that default.
	switch {
	case r.Action.Status == nil:
		m.Status = types.Int64Null()
	case m.Status.IsNull() && *r.Action.Status == edgeDefaultStatus[r.Action.Type]:
		// the default, still unset
	default:
		m.Status = types.Int64Value(*r.Action.Status)
	}
	m.RedirectTarget = types.StringPointerValue(r.Action.Target)
	m.Body = types.StringNull()
	if r.Action.Body != nil && *r.Action.Body != "" {
		m.Body = types.StringValue(*r.Action.Body)
	}
	m.Position = types.Int64Value(r.Position)
	m.Revision = types.Int64Value(r.Revision)
	m.Summary = types.StringValue(r.Summary)
}

func mustUnmarshal(raw json.RawMessage) any {
	var v any
	_ = json.Unmarshal(raw, &v)
	return v
}

func (m *edgeRuleModel) request() client.EdgeRuleRequest {
	action := client.EdgeRuleAction{Type: m.Action.ValueString()}
	if def, ok := edgeDefaultStatus[action.Type]; ok {
		action.Status = ptr(def)
		if known(m.Status) {
			action.Status = int64Ptr(m.Status)
		}
	}
	switch action.Type {
	case "block":
		action.Body = strPtr(m.Body)
	case "redirect":
		action.Target = strPtr(m.RedirectTarget)
	}
	return client.EdgeRuleRequest{
		Name: m.Name.ValueString(), EnvironmentID: int64Ptr(m.EnvironmentID), Enabled: m.Enabled.ValueBool(),
		Mode: m.Mode.ValueString(), Expression: json.RawMessage(m.Expression.ValueString()), Action: action,
	}
}

func (r *edgeRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan edgeRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid := plan.ProjectID.ValueInt64()
	rule, err := r.client.CreateEdgeRule(ctx, pid, plan.request())
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create edge rule "+plan.Name.ValueString()+" in project "+formatID(pid), err, edgeRulePermissions)
		return
	}
	plan.fromAPI(rule)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *edgeRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state edgeRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_edge_rule ID", err.Error())
		return
	}
	rule, err := r.client.GetEdgeRule(ctx, state.ProjectID.ValueInt64(), id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read edge rule "+formatID(id), err, edgeRulePermissions)
		return
	}
	state.fromAPI(rule)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *edgeRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state edgeRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_edge_rule ID", err.Error())
		return
	}
	body := plan.request()
	body.Revision = int64Ptr(state.Revision)
	rule, err := r.client.UpdateEdgeRule(ctx, plan.ProjectID.ValueInt64(), id, body)
	if err != nil {
		if client.IsConflict(err) {
			err = withHint{err, "The rule changed outside Terraform since the last refresh. Run terraform apply again to plan against its current settings."}
		}
		addAPIError(&resp.Diagnostics, "Unable to update edge rule "+formatID(id), err, edgeRulePermissions)
		return
	}
	plan.fromAPI(rule)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *edgeRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state edgeRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_edge_rule ID", err.Error())
		return
	}
	if err := r.client.DeleteEdgeRule(ctx, state.ProjectID.ValueInt64(), id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete edge rule "+formatID(id), err, edgeRulePermissions)
	}
}

func (r *edgeRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	pid, id, err := parseCompositeID(req.ID, "<project_id>/<rule_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), pid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), formatID(id))...)
}
