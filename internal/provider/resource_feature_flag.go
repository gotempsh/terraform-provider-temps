// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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

var flagPermissions = []string{"flags:read", "flags:write", "flags:delete"}

var (
	_ resource.Resource                   = &featureFlagResource{}
	_ resource.ResourceWithImportState    = &featureFlagResource{}
	_ resource.ResourceWithValidateConfig = &featureFlagResource{}
)

type featureFlagResource struct{ resourceBase }

func newFeatureFlagResource() resource.Resource { return &featureFlagResource{} }

type featureFlagModel struct {
	ID            types.String `tfsdk:"id"`
	ProjectID     types.Int64  `tfsdk:"project_id"`
	Key           types.String `tfsdk:"key"`
	ValueType     types.String `tfsdk:"value_type"`
	DefaultValue  types.String `tfsdk:"default_value"`
	Description   types.String `tfsdk:"description"`
	ClientVisible types.Bool   `tfsdk:"client_visible"`
}

// checkFlagValue reports why a JSON value does not fit a flag type, or "".
func checkFlagValue(valueType, raw string) string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return "is not JSON; wrap it in jsonencode()"
	}
	ok := true
	switch valueType {
	case "bool":
		_, ok = v.(bool)
	case "string":
		_, ok = v.(string)
	case "number":
		_, ok = v.(float64)
	case "json":
		ok = v != nil
	}
	if !ok {
		return fmt.Sprintf("is %s, which is not a %s value", raw, valueType)
	}
	return ""
}

func (r *featureFlagResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_feature_flag"
}

func (r *featureFlagResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A project feature flag. Set per-environment values and kill switches with `temps_feature_flag_environment`.\n\n" +
			"Temps never hard-deletes flags: destroying this resource archives the flag, and its key stays reserved. " +
			"Creating a flag whose key belongs to an archived flag of the same `value_type` restores that flag, " +
			"clears its old environment overrides and applies this configuration. Changing `key` or `value_type` therefore " +
			"needs a new key: Temps cannot reuse an archived key for a different type.\n\n" +
			"Import with `<project_id>/<key>`.\n\nRequired API key permissions: " + permList(flagPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "`<project_id>/<key>`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the project.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"key": schema.StringAttribute{Required: true, PlanModifiers: replaceStr,
				MarkdownDescription: "Key used in application code: lower-case letters, digits, `.`, `_` and `-`, starting with a letter or digit, at most 128 characters.",
				Validators: []validator.String{stringvalidator.RegexMatches(flagKeyRE,
					"must match [a-z0-9][a-z0-9._-]* and be at most 128 characters")}},
			"value_type": schema.StringAttribute{Required: true, PlanModifiers: replaceStr,
				MarkdownDescription: "`bool`, `string`, `number` or `json`. Fixed once created.",
				Validators:          []validator.String{stringvalidator.OneOf("bool", "string", "number", "json")}},
			"default_value": schema.StringAttribute{Required: true,
				MarkdownDescription: "JSON-encoded value served when nothing more specific applies, for example `jsonencode(false)`. " +
					"Compared semantically, so key order and number formatting never show as changes.",
				Validators: []validator.String{jsonString{}}},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "What the flag controls.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"client_visible": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Whether browsers may evaluate the flag through the public endpoint. Defaults to `false`."},
		},
	}
}

func (r *featureFlagResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg featureFlagModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || !known(cfg.ValueType) || !known(cfg.DefaultValue) {
		return
	}
	if problem := checkFlagValue(cfg.ValueType.ValueString(), cfg.DefaultValue.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(path.Root("default_value"), "Invalid default_value", "default_value "+problem+".")
	}
}

func (m *featureFlagModel) fromAPI(projectID int64, f *client.FeatureFlag) {
	m.ID = types.StringValue(formatID(projectID) + "/" + f.Key)
	m.ProjectID = types.Int64Value(projectID)
	m.Key = types.StringValue(f.Key)
	m.ValueType = types.StringValue(f.ValueType)
	m.DefaultValue = jsonValue(m.DefaultValue, f.DefaultValue)
	m.Description = types.StringPointerValue(f.Description)
	m.ClientVisible = types.BoolValue(f.ClientVisible)
}

func (r *featureFlagResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan featureFlagModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid, key := plan.ProjectID.ValueInt64(), plan.Key.ValueString()
	f, err := r.client.CreateFeatureFlag(ctx, pid, client.CreateFeatureFlagRequest{
		Key: key, ValueType: plan.ValueType.ValueString(), DefaultValue: json.RawMessage(plan.DefaultValue.ValueString()),
		Description: strPtr(plan.Description), ClientVisible: plan.ClientVisible.ValueBool(),
	})
	if client.IsConflict(err) {
		f, err = r.restoreArchived(ctx, plan, err, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create feature flag "+plan.Key.String()+" in project "+formatID(pid), err, flagPermissions)
		return
	}
	plan.fromAPI(pid, f)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// restoreArchived handles a create that conflicts with an existing key. An
// archived flag of the same type is what destroying this resource leaves
// behind, so it is restored and reset to the configuration; a live flag, or
// an archived one of another type, is reported with what to do.
func (r *featureFlagResource) restoreArchived(ctx context.Context, plan featureFlagModel, conflict error, diags *diag.Diagnostics) (*client.FeatureFlag, error) {
	pid, key := plan.ProjectID.ValueInt64(), plan.Key.ValueString()
	existing, err := r.client.GetFeatureFlag(ctx, pid, key)
	if err != nil {
		return nil, conflict
	}
	if existing.ArchivedAt == nil {
		return nil, withHint{conflict, fmt.Sprintf("Flag %q already exists in project %d. Import it with: terraform import <address> %d/%s", key, pid, pid, key)}
	}
	if existing.ValueType != plan.ValueType.ValueString() {
		return nil, withHint{conflict, fmt.Sprintf("Flag %q was archived as a %s flag, and Temps keeps archived keys reserved and cannot change a flag's type. "+
			"Use a new key for the %s flag.", key, existing.ValueType, plan.ValueType.ValueString())}
	}
	if _, err := r.client.RestoreFeatureFlag(ctx, pid, key); err != nil {
		return nil, err
	}
	for _, e := range existing.Environments {
		if _, err := r.client.SetFeatureFlagEnvironment(ctx, pid, key, e.EnvironmentID, client.SetFeatureFlagEnvironmentRequest{Enabled: true}); err != nil {
			return nil, fmt.Errorf("clearing the restored flag's override in environment %d: %w", e.EnvironmentID, err)
		}
	}
	diags.AddWarning("Restored archived feature flag "+key,
		fmt.Sprintf("Project %d had an archived %s flag %q, so it was restored, its environment overrides were cleared, and it was updated to match the configuration.",
			pid, existing.ValueType, key))
	cv := plan.ClientVisible.ValueBool()
	return r.client.UpdateFeatureFlag(ctx, pid, key, client.UpdateFeatureFlagRequest{
		DefaultValue: json.RawMessage(plan.DefaultValue.ValueString()), Description: strPtr(plan.Description), ClientVisible: &cv,
	})
}

func (r *featureFlagResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state featureFlagModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid, key := state.ProjectID.ValueInt64(), state.Key.ValueString()
	f, err := r.client.GetFeatureFlag(ctx, pid, key)
	if client.IsNotFound(err) || (err == nil && f.ArchivedAt != nil) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read feature flag "+state.Key.String()+" in project "+formatID(pid), err, flagPermissions)
		return
	}
	state.fromAPI(pid, f)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *featureFlagResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan featureFlagModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid, key := plan.ProjectID.ValueInt64(), plan.Key.ValueString()
	cv := plan.ClientVisible.ValueBool()
	f, err := r.client.UpdateFeatureFlag(ctx, pid, key, client.UpdateFeatureFlagRequest{
		DefaultValue: json.RawMessage(plan.DefaultValue.ValueString()), Description: strPtr(plan.Description), ClientVisible: &cv,
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update feature flag "+plan.Key.String()+" in project "+formatID(pid), err, flagPermissions)
		return
	}
	plan.fromAPI(pid, f)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *featureFlagResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state featureFlagModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid := state.ProjectID.ValueInt64()
	if err := r.client.ArchiveFeatureFlag(ctx, pid, state.Key.ValueString()); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to archive feature flag "+state.Key.String()+" in project "+formatID(pid), err, flagPermissions)
	}
}

// parseProjectKeyID parses "<project_id>/<key>[/<environment_id>]".
func parseProjectKeyID(s string, withEnv bool) (int64, string, int64, error) {
	format := "<project_id>/<key>"
	want := 2
	if withEnv {
		format, want = "<project_id>/<key>/<environment_id>", 3
	}
	parts := strings.Split(s, "/")
	if len(parts) != want || parts[1] == "" {
		return 0, "", 0, fmt.Errorf("import ID %q must have the form %s", s, format)
	}
	pid, err := parseID(parts[0])
	if err != nil {
		return 0, "", 0, fmt.Errorf("import ID %q must have the form %s: %w", s, format, err)
	}
	var env int64
	if withEnv {
		if env, err = parseID(parts[2]); err != nil {
			return 0, "", 0, fmt.Errorf("import ID %q must have the form %s: %w", s, format, err)
		}
	}
	return pid, parts[1], env, nil
}

func (r *featureFlagResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	pid, key, _, err := parseProjectKeyID(req.ID, false)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), pid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("key"), key)...)
}
