// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var environmentPermissions = []string{"environments:create", "environments:read", "environments:write", "environments:delete"}

var (
	_ resource.Resource                = &environmentResource{}
	_ resource.ResourceWithImportState = &environmentResource{}
)

type environmentResource struct{ resourceBase }

func newEnvironmentResource() resource.Resource { return &environmentResource{} }

type environmentModel struct {
	ID         types.String `tfsdk:"id"`
	ProjectID  types.Int64  `tfsdk:"project_id"`
	Name       types.String `tfsdk:"name"`
	Branch     types.String `tfsdk:"branch"`
	Preview    types.Bool   `tfsdk:"preview"`
	Protected  types.Bool   `tfsdk:"protected"`
	ForceHTTPS types.Bool   `tfsdk:"force_https"`
	Slug       types.String `tfsdk:"slug"`
	Subdomain  types.String `tfsdk:"subdomain"`
	MainURL    types.String `tfsdk:"main_url"`
}

func (r *environmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_environment"
}

func (r *environmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "An additional environment of a project (for example `staging`). The `production` " +
			"environment Temps creates with every project is not managed here: read it with the `temps_environment` data source.\n\n" +
			"Import with `<project_id>/<environment_id>`.\n\nRequired API key permissions: " + permList(environmentPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "Numeric environment ID."},
			"project_id": schema.Int64Attribute{
				Required: true, MarkdownDescription: "ID of the project. Changing it replaces the environment.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required: true, MarkdownDescription: "Environment name. Temps cannot rename environments, so changing it replaces the environment.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 63)},
			},
			"branch": schema.StringAttribute{
				Required: true, MarkdownDescription: "Git branch deployed to this environment.",
			},
			"preview": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Create the environment as the project's preview environment. Changing it replaces the environment.",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"protected": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Require a password to reach the environment.",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"force_https": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Redirect HTTP requests to HTTPS. Unset means the server default.",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"slug":      schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "Environment slug."},
			"subdomain": schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "Subdomain the environment is served on."},
			"main_url":  schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "Primary URL of the environment."},
		},
	}
}

func (m *environmentModel) fromAPI(e *client.Environment) {
	m.ID = types.StringValue(formatID(e.ID))
	m.ProjectID = types.Int64Value(e.ProjectID)
	m.Name = types.StringValue(e.Name)
	branch := ""
	if e.Branch != nil {
		branch = *e.Branch
	}
	m.Branch = types.StringValue(branch)
	m.Preview = types.BoolValue(e.IsPreview)
	m.Protected = types.BoolValue(e.Protected)
	m.ForceHTTPS = types.BoolValue(e.ForceHTTPS != nil && *e.ForceHTTPS)
	m.Slug = types.StringValue(e.Slug)
	m.Subdomain = types.StringValue(e.Subdomain)
	m.MainURL = types.StringValue(e.MainURL)
}

func (r *environmentResource) ids(m *environmentModel) (int64, int64, error) {
	id, err := parseID(m.ID.ValueString())
	return m.ProjectID.ValueInt64(), id, err
}

func (r *environmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan environmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid := plan.ProjectID.ValueInt64()
	e, err := r.client.CreateEnvironment(ctx, pid, client.CreateEnvironmentRequest{
		Name: plan.Name.ValueString(), Branch: plan.Branch.ValueString(), SetAsPreview: plan.Preview.ValueBool(),
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create environment "+plan.Name.String()+" in Temps project "+formatID(pid), err, environmentPermissions)
		return
	}
	if known(plan.Protected) || known(plan.ForceHTTPS) {
		updated, err := r.client.UpdateEnvironmentSettings(ctx, pid, e.ID, client.UpdateEnvironmentSettingsRequest{
			Protected: boolPtr(plan.Protected), ForceHTTPS: boolPtr(plan.ForceHTTPS),
		})
		if err != nil {
			plan.fromAPI(e) // keep the created environment tracked
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			addAPIError(&resp.Diagnostics, "Created environment "+formatID(e.ID)+" but could not apply its settings", err, environmentPermissions)
			return
		}
		e = updated
	}
	plan.fromAPI(e)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *environmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid, id, err := r.ids(&state)
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_environment ID", err.Error())
		return
	}
	e, err := r.client.GetEnvironment(ctx, pid, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read Temps environment "+formatID(id), err, environmentPermissions)
		return
	}
	state.fromAPI(e)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *environmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state environmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid, id, err := r.ids(&state)
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_environment ID", err.Error())
		return
	}
	upd := client.UpdateEnvironmentSettingsRequest{}
	if !plan.Branch.Equal(state.Branch) {
		upd.Branch = strPtr(plan.Branch)
	}
	if known(plan.Protected) && !plan.Protected.Equal(state.Protected) {
		upd.Protected = boolPtr(plan.Protected)
	}
	if known(plan.ForceHTTPS) && !plan.ForceHTTPS.Equal(state.ForceHTTPS) {
		upd.ForceHTTPS = boolPtr(plan.ForceHTTPS)
	}
	e, err := r.client.UpdateEnvironmentSettings(ctx, pid, id, upd)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update Temps environment "+formatID(id), err, environmentPermissions)
		return
	}
	plan.fromAPI(e)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *environmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid, id, err := r.ids(&state)
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_environment ID", err.Error())
		return
	}
	if err := r.client.DeleteEnvironment(ctx, pid, id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete Temps environment "+formatID(id), err, environmentPermissions)
	}
}

func (r *environmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	pid, id, err := parseCompositeID(req.ID, "<project_id>/<environment_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), pid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), formatID(id))...)
}
