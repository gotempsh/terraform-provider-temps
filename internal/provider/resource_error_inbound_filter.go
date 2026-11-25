// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var inboundFilterPermissions = []string{"error_tracking:read", "error_tracking:write"}

// Kinds that match by a pattern; the others are built-in rules that take none.
var (
	inboundFilterPatternKinds = []string{"error_message", "release"}
	inboundFilterKinds        = []string{"browser_extensions", "web_crawlers", "localhost", "error_message", "release"}
)

// inboundFilterMaxPattern is the server's limit, in characters.
const inboundFilterMaxPattern = 200

var (
	_ resource.Resource                   = &inboundFilterResource{}
	_ resource.ResourceWithImportState    = &inboundFilterResource{}
	_ resource.ResourceWithValidateConfig = &inboundFilterResource{}
)

type inboundFilterResource struct{ resourceBase }

func newInboundFilterResource() resource.Resource { return &inboundFilterResource{} }

type inboundFilterModel struct {
	ID           types.String `tfsdk:"id"`
	ProjectID    types.Int64  `tfsdk:"project_id"`
	Kind         types.String `tfsdk:"kind"`
	Pattern      types.String `tfsdk:"pattern"`
	Enabled      types.Bool   `tfsdk:"enabled"`
	DroppedCount types.Int64  `tfsdk:"dropped_count"`
}

func (r *inboundFilterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_error_inbound_filter"
}

func (r *inboundFilterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A rule that drops matching error events of a project before they are stored, so they " +
			"neither count against quotas nor trigger alerts. It applies from the next event.\n\n" +
			"The built-in kinds (`browser_extensions`, `web_crawlers`, `localhost`) can exist once per project; " +
			"`error_message` and `release` rules match a pattern and can be repeated.\n\n" +
			"Import with `<project_id>/<filter_id>`.\n\n" +
			"Required API key permissions: " + permList(inboundFilterPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric filter ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the project. Changing it replaces the filter.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"kind": schema.StringAttribute{Required: true,
				MarkdownDescription: "What the rule drops: `" + joinTicks(inboundFilterKinds) + "`. Changing it replaces the filter.",
				Validators:          []validator.String{stringvalidator.OneOf(inboundFilterKinds...)},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"pattern": schema.StringAttribute{Optional: true,
				MarkdownDescription: fmt.Sprintf("Glob, required for `error_message` (matched against `Type: message`) and "+
					"`release`, and not allowed for the other kinds. `*` matches any run of characters and `?` one; matching "+
					"ignores case. At most %d characters.", inboundFilterMaxPattern),
				Validators: []validator.String{notBlank{}}},
			"enabled": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "Whether the rule drops events. Defaults to `true`."},
			"dropped_count": schema.Int64Attribute{Computed: true,
				MarkdownDescription: "Events the rule dropped since it was created, as of the last refresh.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
		},
	}
}

func (r *inboundFilterResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg inboundFilterModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || !known(cfg.Kind) || cfg.Pattern.IsUnknown() {
		return
	}
	kind := cfg.Kind.ValueString()
	needsPattern := slices.Contains(inboundFilterPatternKinds, kind)
	switch {
	case needsPattern && cfg.Pattern.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("pattern"), "Missing pattern",
			fmt.Sprintf("An inbound filter of kind %q needs a pattern, such as \"*ResizeObserver*\".", kind))
	case !needsPattern && !cfg.Pattern.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("pattern"), "Pattern not allowed",
			fmt.Sprintf("An inbound filter of kind %q is built in and takes no pattern; patterns apply to `%s`.",
				kind, joinTicks(inboundFilterPatternKinds)))
	case !cfg.Pattern.IsNull():
		p := cfg.Pattern.ValueString()
		if n := utf8.RuneCountInString(p); n > inboundFilterMaxPattern {
			resp.Diagnostics.AddAttributeError(path.Root("pattern"), "Pattern too long",
				fmt.Sprintf("The pattern is %d characters; Temps allows at most %d.", n, inboundFilterMaxPattern))
		}
		if strings.ContainsRune(p, 0) {
			resp.Diagnostics.AddAttributeError(path.Root("pattern"), "Invalid pattern", "The pattern contains a NUL character.")
		}
		if strings.Trim(p, "*") == "" {
			resp.Diagnostics.AddAttributeWarning(path.Root("pattern"), "Pattern matches everything",
				fmt.Sprintf("The pattern %q matches every %s, so this filter drops every error event of the project.",
					p, strings.ReplaceAll(kind, "_", " ")))
		}
	}
}

func (m *inboundFilterModel) fromAPI(f *client.InboundFilter) {
	m.ID = types.StringValue(formatID(f.ID))
	m.ProjectID = types.Int64Value(f.ProjectID)
	m.Kind = types.StringValue(f.Kind)
	m.Pattern = types.StringPointerValue(f.Pattern)
	m.Enabled = types.BoolValue(f.Enabled)
	m.DroppedCount = types.Int64Value(f.DroppedCount)
}

func (r *inboundFilterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan inboundFilterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid, kind := plan.ProjectID.ValueInt64(), plan.Kind.ValueString()
	f, err := r.client.CreateInboundFilter(ctx, pid, client.InboundFilterRequest{
		Kind: kind, Pattern: strPtr(plan.Pattern), Enabled: boolPtr(plan.Enabled),
	})
	if client.IsConflict(err) {
		err = withHint{err, fmt.Sprintf("Project %d already has a %q filter. Import it with `terraform import <address> %d/<filter_id>` "+
			"instead of creating another.", pid, kind, pid)}
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to create %s inbound filter in project %d", kind, pid), err, inboundFilterPermissions)
		return
	}
	plan.fromAPI(f)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *inboundFilterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state inboundFilterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_error_inbound_filter ID", err.Error())
		return
	}
	f, err := r.client.GetInboundFilter(ctx, state.ProjectID.ValueInt64(), id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read inbound filter "+formatID(id), err, inboundFilterPermissions)
		return
	}
	state.fromAPI(f)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *inboundFilterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state inboundFilterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_error_inbound_filter ID", err.Error())
		return
	}
	// The kind cannot change in place, and a pattern kind always has a
	// pattern, so the pattern is never cleared here.
	f, err := r.client.UpdateInboundFilter(ctx, plan.ProjectID.ValueInt64(), id, client.InboundFilterRequest{
		Pattern: strPtr(plan.Pattern), Enabled: boolPtr(plan.Enabled),
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update inbound filter "+formatID(id), err, inboundFilterPermissions)
		return
	}
	plan.fromAPI(f)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *inboundFilterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state inboundFilterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_error_inbound_filter ID", err.Error())
		return
	}
	if err := r.client.DeleteInboundFilter(ctx, state.ProjectID.ValueInt64(), id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete inbound filter "+formatID(id), err, inboundFilterPermissions)
	}
}

func (r *inboundFilterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	pid, id, err := parseCompositeID(req.ID, "<project_id>/<filter_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), pid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), formatID(id))...)
}
