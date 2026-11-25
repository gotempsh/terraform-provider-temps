// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var projectSecretPermissions = []string{"environments:read", "environments:create", "environments:write", "environments:delete"}

var (
	_ resource.Resource                = &projectSecretResource{}
	_ resource.ResourceWithImportState = &projectSecretResource{}
)

type projectSecretResource struct{ resourceBase }

func newProjectSecretResource() resource.Resource { return &projectSecretResource{} }

type projectSecretModel struct {
	ID               types.String `tfsdk:"id"`
	ProjectID        types.Int64  `tfsdk:"project_id"`
	Key              types.String `tfsdk:"key"`
	Value            types.String `tfsdk:"value"`
	EnvironmentIDs   types.Set    `tfsdk:"environment_ids"`
	IncludeInPreview types.Bool   `tfsdk:"include_in_preview"`
	ComposeServices  types.Set    `tfsdk:"compose_services"`
}

var secretKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (r *projectSecretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_secret"
}

func (r *projectSecretResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An encrypted secret of a project, injected into its deployments.\n\n" +
			"The value is write-only on the server: the provider keeps the configured value in state and cannot detect " +
			"a change made outside Terraform. Every update sends the whole scope, because Temps treats an omitted scope as " +
			"\"every environment\".\n\n" +
			"Import with `<project_id>/<secret_id>`; the value cannot be imported and is set again on the next apply.\n\n" +
			"Required API key permissions: " + permList(projectSecretPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric secret ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the project. Changing it replaces the secret.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"key": schema.StringAttribute{Required: true,
				MarkdownDescription: "Secret name, for example `API_TOKEN`. Changing it replaces the secret.",
				Validators: []validator.String{stringvalidator.RegexMatches(secretKeyPattern,
					"must start with a letter or underscore and contain only letters, digits and underscores")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"value": schema.StringAttribute{Required: true, Sensitive: true, MarkdownDescription: "Secret value."},
			"environment_ids": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.Int64Type,
				Default: setdefault.StaticValue(types.SetValueMust(types.Int64Type, nil)),
				MarkdownDescription: "IDs of the environments the secret applies to. Empty (the default) applies it to every " +
					"environment of the project. Two secrets with the same key cannot share an environment."},
			"include_in_preview": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Also inject the secret into preview environments."},
			"compose_services": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType,
				Default: setdefault.StaticValue(types.SetValueMust(types.StringType, nil)),
				MarkdownDescription: "Docker Compose services that receive the secret. Empty (the default) means all of them. " +
					"Temps does not check that the services exist.",
				Validators: []validator.Set{setvalidator.ValueStringsAre(stringvalidator.LengthBetween(1, 255))}},
		},
	}
}

func (m *projectSecretModel) fromAPI(s *client.ProjectSecret) {
	m.ID = types.StringValue(formatID(s.ID))
	m.ProjectID = types.Int64Value(s.ProjectID)
	m.Key = types.StringValue(s.Key)
	m.EnvironmentIDs = int64SetValue(s.EnvironmentIDs())
	m.IncludeInPreview = types.BoolValue(s.IncludeInPreview)
	m.ComposeServices = stringSetValue(s.ComposeServices)
}

func (m *projectSecretModel) request(ctx context.Context, value bool, diags *diag.Diagnostics) client.ProjectSecretRequest {
	req := client.ProjectSecretRequest{
		Key:              m.Key.ValueString(),
		EnvironmentIDs:   int64sFromSet(ctx, m.EnvironmentIDs, diags),
		IncludeInPreview: m.IncludeInPreview.ValueBool(),
		ComposeServices:  stringsFromSet(ctx, m.ComposeServices, diags),
	}
	if req.EnvironmentIDs == nil {
		req.EnvironmentIDs = []int64{}
	}
	if value {
		req.Value = strPtr(m.Value)
	}
	return req
}

func (r *projectSecretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectSecretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := plan.request(ctx, true, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	pid := plan.ProjectID.ValueInt64()
	s, err := r.client.CreateProjectSecret(ctx, pid, body)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create secret "+plan.Key.ValueString()+" in project "+formatID(pid), err, projectSecretPermissions)
		return
	}
	plan.fromAPI(s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectSecretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectSecretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_project_secret ID", err.Error())
		return
	}
	s, err := r.client.GetProjectSecret(ctx, state.ProjectID.ValueInt64(), id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read secret "+formatID(id), err, projectSecretPermissions)
		return
	}
	state.fromAPI(s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *projectSecretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state projectSecretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_project_secret ID", err.Error())
		return
	}
	// Send the value only when it changed, but always the full scope.
	body := plan.request(ctx, !plan.Value.Equal(state.Value), &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	s, err := r.client.UpdateProjectSecret(ctx, plan.ProjectID.ValueInt64(), id, body)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update secret "+formatID(id), err, projectSecretPermissions)
		return
	}
	plan.fromAPI(s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectSecretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectSecretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_project_secret ID", err.Error())
		return
	}
	if err := r.client.DeleteProjectSecret(ctx, state.ProjectID.ValueInt64(), id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete secret "+formatID(id), err, projectSecretPermissions)
	}
}

func (r *projectSecretResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	pid, id, err := parseCompositeID(req.ID, "<project_id>/<secret_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), pid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), formatID(id))...)
}
