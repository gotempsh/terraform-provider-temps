// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var projectPermissions = []string{"projects:create", "projects:read", "projects:write", "projects:delete"}

var (
	_ resource.Resource                = &projectResource{}
	_ resource.ResourceWithImportState = &projectResource{}
)

type projectResource struct{ resourceBase }

func newProjectResource() resource.Resource { return &projectResource{} }

type projectModel struct {
	ID                        types.String `tfsdk:"id"`
	Name                      types.String `tfsdk:"name"`
	Slug                      types.String `tfsdk:"slug"`
	SourceType                types.String `tfsdk:"source_type"`
	GitURL                    types.String `tfsdk:"git_url"`
	RepoOwner                 types.String `tfsdk:"repo_owner"`
	RepoName                  types.String `tfsdk:"repo_name"`
	GitProviderConnectionID   types.Int64  `tfsdk:"git_provider_connection_id"`
	IsPublicRepo              types.Bool   `tfsdk:"is_public_repo"`
	MainBranch                types.String `tfsdk:"main_branch"`
	Directory                 types.String `tfsdk:"directory"`
	Preset                    types.String `tfsdk:"preset"`
	BuildCommand              types.String `tfsdk:"build_command"`
	InstallCommand            types.String `tfsdk:"install_command"`
	OutputDir                 types.String `tfsdk:"output_dir"`
	BuildContext              types.String `tfsdk:"build_context"`
	DockerfilePath            types.String `tfsdk:"dockerfile_path"`
	ExposedPort               types.Int64  `tfsdk:"exposed_port"`
	AutomaticDeploy           types.Bool   `tfsdk:"automatic_deploy"`
	EnablePreviewEnvironments types.Bool   `tfsdk:"enable_preview_environments"`
	ProjectType               types.String `tfsdk:"project_type"`
}

var sourceTypes = []string{"git", "docker_image", "static_files", "manual", "external", "uploaded_source"}

func (r *projectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (r *projectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Temps project: an application with its source, build settings and deploy behaviour. " +
			"Temps creates a `production` environment for every project; read it with the `temps_environment` data source.\n\n" +
			"Required API key permissions: " + permList(projectPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric project ID.", PlanModifiers: keep},
			"name": schema.StringAttribute{
				Required: true, MarkdownDescription: "Display name, unique per Temps installation.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 255)},
			},
			"slug": schema.StringAttribute{
				Optional: true, Computed: true, PlanModifiers: keep,
				MarkdownDescription: "URL slug used in environment hostnames. Derived from `name` when omitted.",
			},
			"source_type": schema.StringAttribute{
				Optional: true, Computed: true, PlanModifiers: keep,
				MarkdownDescription: "Where the code comes from: one of `" + strings.Join(sourceTypes, "`, `") +
					"`. Temps defaults to `git`, which needs `repo_owner` and `repo_name`.",
				Validators: []validator.String{stringvalidator.OneOf(sourceTypes...)},
			},
			"git_url": schema.StringAttribute{
				Optional: true, MarkdownDescription: "Clone URL of the repository. Changing it replaces the project.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"repo_owner": schema.StringAttribute{Optional: true, MarkdownDescription: "Repository owner or group."},
			"repo_name":  schema.StringAttribute{Optional: true, MarkdownDescription: "Repository name."},
			"git_provider_connection_id": schema.Int64Attribute{
				Optional: true, MarkdownDescription: "ID of the Git provider connection used to clone private repositories.",
			},
			"is_public_repo": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Whether the repository is public (cloned without credentials). Changing it replaces the project.",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown(), boolplanmodifier.RequiresReplace()},
			},
			"main_branch": schema.StringAttribute{
				Optional: true, Computed: true, PlanModifiers: keep,
				MarkdownDescription: "Branch deployed to production.",
			},
			"directory": schema.StringAttribute{
				Optional: true, Computed: true, PlanModifiers: keep,
				MarkdownDescription: "Directory inside the repository that holds the app. Defaults to `.`.",
			},
			"preset": schema.StringAttribute{
				Optional: true, Computed: true, PlanModifiers: keep,
				MarkdownDescription: "Build preset, for example `dockerfile`, `nixpacks`, `vite` or `nextjs`.",
			},
			"build_command":   schema.StringAttribute{Optional: true, MarkdownDescription: "Build command override (presets that build with a package manager)."},
			"install_command": schema.StringAttribute{Optional: true, MarkdownDescription: "Install command override."},
			"output_dir":      schema.StringAttribute{Optional: true, MarkdownDescription: "Build output directory (static presets)."},
			"build_context":   schema.StringAttribute{Optional: true, MarkdownDescription: "Docker build context directory."},
			"dockerfile_path": schema.StringAttribute{Optional: true, MarkdownDescription: "Path of the Dockerfile (`dockerfile` preset)."},
			"exposed_port": schema.Int64Attribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Port the application listens on inside its container. Removing it from the configuration keeps the current value.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
				Validators:          []validator.Int64{int64validator.Between(1, 65535)},
			},
			"automatic_deploy": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Deploy automatically when the main branch receives a push.",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"enable_preview_environments": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Create a preview environment for each branch or pull request.",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"project_type": schema.StringAttribute{
				Computed: true, PlanModifiers: keep,
				MarkdownDescription: "Kind of project Temps derived from the preset (`server`, `static`, ...).",
			},
		},
	}
}

func (m *projectModel) presetConfig() *client.PresetConfig {
	if !known(m.BuildCommand) && !known(m.InstallCommand) && !known(m.OutputDir) && !known(m.BuildContext) && !known(m.DockerfilePath) {
		return nil
	}
	return &client.PresetConfig{
		Preset:         m.Preset.ValueString(),
		BuildCommand:   strPtr(m.BuildCommand),
		InstallCommand: strPtr(m.InstallCommand),
		OutputDir:      strPtr(m.OutputDir),
		BuildContext:   strPtr(m.BuildContext),
		DockerfilePath: strPtr(m.DockerfilePath),
	}
}

func (m *projectModel) fromAPI(p *client.Project) {
	m.ID = types.StringValue(formatID(p.ID))
	m.Name = types.StringValue(p.Name)
	m.Slug = types.StringValue(p.Slug)
	m.SourceType = types.StringValue(p.SourceType)
	m.GitURL = types.StringPointerValue(p.GitURL)
	m.RepoOwner = types.StringPointerValue(p.RepoOwner)
	m.RepoName = types.StringPointerValue(p.RepoName)
	m.GitProviderConnectionID = types.Int64PointerValue(p.GitProviderConnectionID)
	m.IsPublicRepo = types.BoolValue(p.IsPublicRepo)
	m.MainBranch = types.StringValue(p.MainBranch)
	m.Directory = types.StringValue(p.Directory)
	m.Preset = types.StringPointerValue(p.Preset)
	pc := p.PresetConfig
	if pc == nil {
		pc = &client.PresetConfig{}
	}
	m.BuildCommand = types.StringPointerValue(pc.BuildCommand)
	m.InstallCommand = types.StringPointerValue(pc.InstallCommand)
	m.OutputDir = types.StringPointerValue(pc.OutputDir)
	m.BuildContext = types.StringPointerValue(pc.BuildContext)
	m.DockerfilePath = types.StringPointerValue(pc.DockerfilePath)
	m.ExposedPort = types.Int64PointerValue(p.DeploymentConfig.ExposedPort)
	m.AutomaticDeploy = types.BoolValue(p.DeploymentConfig.AutomaticDeploy != nil && *p.DeploymentConfig.AutomaticDeploy)
	m.EnablePreviewEnvironments = types.BoolValue(p.EnablePreviewEnvironments)
	m.ProjectType = types.StringValue(p.ProjectType)
}

func (r *projectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pc := plan.presetConfig()
	if pc != nil && !known(plan.Preset) {
		resp.Diagnostics.AddAttributeError(path.Root("preset"), "preset is required with build settings",
			"build_command, install_command, output_dir, build_context and dockerfile_path belong to a preset; set preset as well.")
		return
	}
	p, err := r.client.CreateProject(ctx, client.CreateProjectRequest{
		Name:                    plan.Name.ValueString(),
		ExpectedSlug:            strPtr(plan.Slug),
		SourceType:              strPtr(plan.SourceType),
		GitURL:                  strPtr(plan.GitURL),
		RepoOwner:               strPtr(plan.RepoOwner),
		RepoName:                strPtr(plan.RepoName),
		GitProviderConnectionID: int64Ptr(plan.GitProviderConnectionID),
		IsPublicRepo:            boolPtr(plan.IsPublicRepo),
		MainBranch:              strPtr(plan.MainBranch),
		Directory:               strPtr(plan.Directory),
		Preset:                  strPtr(plan.Preset),
		PresetConfig:            pc,
		ExposedPort:             int64Ptr(plan.ExposedPort),
		AutomaticDeploy:         boolPtr(plan.AutomaticDeploy),
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create Temps project "+plan.Name.String(), err, projectPermissions)
		return
	}
	// Preview environments are not part of the create request.
	if known(plan.EnablePreviewEnvironments) && plan.EnablePreviewEnvironments.ValueBool() != p.EnablePreviewEnvironments {
		updated, err := r.client.UpdateProjectSettings(ctx, p.ID, client.UpdateProjectSettingsRequest{
			EnablePreviewEnvironments: boolPtr(plan.EnablePreviewEnvironments),
		})
		if err != nil {
			// Keep the created project in state so Terraform can retry or destroy it.
			plan.fromAPI(p)
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			addAPIError(&resp.Diagnostics, "Created Temps project "+formatID(p.ID)+" but could not set enable_preview_environments", err, projectPermissions)
			return
		}
		p = updated
	}
	plan.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_project ID", err.Error())
		return
	}
	p, err := r.client.GetProject(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read Temps project "+state.ID.ValueString(), err, projectPermissions)
		return
	}
	state.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *projectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_project ID", err.Error())
		return
	}
	fail := func(what string, err error) {
		addAPIError(&resp.Diagnostics, "Unable to update "+what+" of Temps project "+state.ID.ValueString(), err, projectPermissions)
	}

	if known(plan.SourceType) && !plan.SourceType.Equal(state.SourceType) {
		if _, err := r.client.ChangeProjectSource(ctx, id, plan.SourceType.ValueString()); err != nil {
			fail("source_type", err)
			return
		}
	}

	settings := client.UpdateProjectSettingsRequest{}
	changed := false
	setStr := func(planV, stateV types.String, dst **string) {
		if known(planV) && !planV.Equal(stateV) {
			*dst = strPtr(planV)
			changed = true
		}
	}
	setStr(plan.Name, state.Name, &settings.Name)
	setStr(plan.Slug, state.Slug, &settings.Slug)
	setStr(plan.RepoOwner, state.RepoOwner, &settings.RepoOwner)
	setStr(plan.RepoName, state.RepoName, &settings.RepoName)
	setStr(plan.MainBranch, state.MainBranch, &settings.MainBranch)
	setStr(plan.Directory, state.Directory, &settings.Directory)
	if known(plan.GitProviderConnectionID) && !plan.GitProviderConnectionID.Equal(state.GitProviderConnectionID) {
		settings.GitProviderConnectionID = int64Ptr(plan.GitProviderConnectionID)
		changed = true
	}
	if known(plan.EnablePreviewEnvironments) && !plan.EnablePreviewEnvironments.Equal(state.EnablePreviewEnvironments) {
		settings.EnablePreviewEnvironments = boolPtr(plan.EnablePreviewEnvironments)
		changed = true
	}
	presetChanged := !plan.Preset.Equal(state.Preset) || !plan.BuildCommand.Equal(state.BuildCommand) ||
		!plan.InstallCommand.Equal(state.InstallCommand) || !plan.OutputDir.Equal(state.OutputDir) ||
		!plan.BuildContext.Equal(state.BuildContext) || !plan.DockerfilePath.Equal(state.DockerfilePath)
	if presetChanged && known(plan.Preset) {
		// The server replaces preset_config wholesale: always send all of it.
		settings.Preset = strPtr(plan.Preset)
		settings.PresetConfig = plan.presetConfig()
		if settings.PresetConfig == nil {
			settings.PresetConfig = &client.PresetConfig{Preset: plan.Preset.ValueString()}
		}
		changed = true
	}
	if changed {
		if _, err := r.client.UpdateProjectSettings(ctx, id, settings); err != nil {
			fail("settings", err)
			return
		}
	}
	if known(plan.AutomaticDeploy) && !plan.AutomaticDeploy.Equal(state.AutomaticDeploy) {
		if _, err := r.client.SetProjectAutomaticDeploy(ctx, id, plan.AutomaticDeploy.ValueBool()); err != nil {
			fail("automatic_deploy", err)
			return
		}
	}
	if known(plan.ExposedPort) && !plan.ExposedPort.Equal(state.ExposedPort) {
		if _, err := r.client.SetProjectExposedPort(ctx, id, plan.ExposedPort.ValueInt64()); err != nil {
			fail("exposed_port", err)
			return
		}
	}

	p, err := r.client.GetProject(ctx, id)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read Temps project "+state.ID.ValueString()+" after update", err, projectPermissions)
		return
	}
	plan.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_project ID", err.Error())
		return
	}
	if err := r.client.DeleteProject(ctx, id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete Temps project "+state.ID.ValueString(), err, projectPermissions)
	}
}

// ImportState accepts a numeric project ID or a project slug.
func (r *projectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := parseID(req.ID); err == nil {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
		return
	}
	p, err := r.client.GetProjectBySlug(ctx, req.ID)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to import Temps project with slug "+req.ID, err, projectPermissions)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), formatID(p.ID))...)
}

func permList(perms []string) string {
	return "`" + strings.Join(perms, "`, `") + "`"
}
