// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

// ---------------------------------------------------------------- temps_project

type projectDataSource struct{ dataSourceBase }

func newProjectDataSource() datasource.DataSource { return &projectDataSource{} }

type projectDataModel struct {
	ID         types.String `tfsdk:"id"`
	Slug       types.String `tfsdk:"slug"`
	Name       types.String `tfsdk:"name"`
	SourceType types.String `tfsdk:"source_type"`
	MainBranch types.String `tfsdk:"main_branch"`
	Preset     types.String `tfsdk:"preset"`
	GitURL     types.String `tfsdk:"git_url"`
}

func (d *projectDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (d *projectDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a project by `id` or `slug`. Needs the `projects:read` permission.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Numeric project ID."},
			"slug":        schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Project slug."},
			"name":        schema.StringAttribute{Computed: true, MarkdownDescription: "Project name."},
			"source_type": schema.StringAttribute{Computed: true, MarkdownDescription: "Source type."},
			"main_branch": schema.StringAttribute{Computed: true, MarkdownDescription: "Production branch."},
			"preset":      schema.StringAttribute{Computed: true, MarkdownDescription: "Build preset."},
			"git_url":     schema.StringAttribute{Computed: true, MarkdownDescription: "Repository clone URL."},
		},
	}
}

func (d *projectDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("slug"))}
}

func (d *projectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg projectDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var p *client.Project
	var err error
	if known(cfg.ID) {
		id, perr := parseID(cfg.ID.ValueString())
		if perr != nil {
			resp.Diagnostics.AddAttributeError(path.Root("id"), "Invalid project ID", perr.Error())
			return
		}
		p, err = d.client.GetProject(ctx, id)
	} else {
		p, err = d.client.GetProjectBySlug(ctx, cfg.Slug.ValueString())
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read Temps project", err, []string{"projects:read"})
		return
	}
	cfg.ID = types.StringValue(formatID(p.ID))
	cfg.Slug = types.StringValue(p.Slug)
	cfg.Name = types.StringValue(p.Name)
	cfg.SourceType = types.StringValue(p.SourceType)
	cfg.MainBranch = types.StringValue(p.MainBranch)
	cfg.Preset = types.StringPointerValue(p.Preset)
	cfg.GitURL = types.StringPointerValue(p.GitURL)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// ------------------------------------------------------------ temps_environment

type environmentDataSource struct{ dataSourceBase }

func newEnvironmentDataSource() datasource.DataSource { return &environmentDataSource{} }

type environmentDataModel struct {
	ID        types.String `tfsdk:"id"`
	ProjectID types.Int64  `tfsdk:"project_id"`
	Name      types.String `tfsdk:"name"`
	Slug      types.String `tfsdk:"slug"`
	Branch    types.String `tfsdk:"branch"`
	MainURL   types.String `tfsdk:"main_url"`
	Subdomain types.String `tfsdk:"subdomain"`
	IsPreview types.Bool   `tfsdk:"is_preview"`
}

func (d *environmentDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_environment"
}

func (d *environmentDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a project environment by name, typically the `production` environment Temps creates " +
			"with every project. Needs the `environments:read` permission.",
		Attributes: map[string]schema.Attribute{
			"project_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the project."},
			"name":       schema.StringAttribute{Required: true, MarkdownDescription: "Environment name or slug, for example `production`."},
			"id":         schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric environment ID."},
			"slug":       schema.StringAttribute{Computed: true, MarkdownDescription: "Environment slug."},
			"branch":     schema.StringAttribute{Computed: true, MarkdownDescription: "Deployed branch."},
			"main_url":   schema.StringAttribute{Computed: true, MarkdownDescription: "Primary URL."},
			"subdomain":  schema.StringAttribute{Computed: true, MarkdownDescription: "Subdomain."},
			"is_preview": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether it is a preview environment."},
		},
	}
}

func (d *environmentDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg environmentDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid := cfg.ProjectID.ValueInt64()
	envs, err := d.client.ListEnvironments(ctx, pid)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to list environments of Temps project "+formatID(pid), err, []string{"environments:read"})
		return
	}
	name := cfg.Name.ValueString()
	idx := slices.IndexFunc(envs, func(e client.Environment) bool { return e.Name == name || e.Slug == name })
	if idx < 0 {
		names := make([]string, len(envs))
		for i, e := range envs {
			names[i] = e.Name
		}
		resp.Diagnostics.AddError("Environment not found",
			"Temps project "+formatID(pid)+" has no environment named "+cfg.Name.String()+"; it has: "+joinQuoted(names)+".")
		return
	}
	e := envs[idx]
	cfg.ID = types.StringValue(formatID(e.ID))
	cfg.Slug = types.StringValue(e.Slug)
	branch := ""
	if e.Branch != nil {
		branch = *e.Branch
	}
	cfg.Branch = types.StringValue(branch)
	cfg.MainURL = types.StringValue(e.MainURL)
	cfg.Subdomain = types.StringValue(e.Subdomain)
	cfg.IsPreview = types.BoolValue(e.IsPreview)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

func joinQuoted(vals []string) string {
	out := ""
	for i, v := range vals {
		if i > 0 {
			out += ", "
		}
		out += "\"" + v + "\""
	}
	if out == "" {
		return "none"
	}
	return out
}

// ---------------------------------------------------------------- temps_service

type serviceDataSource struct{ dataSourceBase }

func newServiceDataSource() datasource.DataSource { return &serviceDataSource{} }

type serviceDataModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	ServiceType types.String `tfsdk:"service_type"`
	Version     types.String `tfsdk:"version"`
	Topology    types.String `tfsdk:"topology"`
	Status      types.String `tfsdk:"status"`
}

func (d *serviceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service"
}

func (d *serviceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a managed service by `id` or `name`. Needs the `external_services:read` permission.",
		Attributes: map[string]schema.Attribute{
			"id":           schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Numeric service ID."},
			"name":         schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Service name."},
			"service_type": schema.StringAttribute{Computed: true, MarkdownDescription: "Service type."},
			"version":      schema.StringAttribute{Computed: true, MarkdownDescription: "Engine version."},
			"topology":     schema.StringAttribute{Computed: true, MarkdownDescription: "Topology."},
			"status":       schema.StringAttribute{Computed: true, MarkdownDescription: "Current status."},
		},
	}
}

func (d *serviceDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name"))}
}

func (d *serviceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg serviceDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	perms := []string{"external_services:read"}
	var s *client.Service
	if known(cfg.ID) {
		id, err := parseID(cfg.ID.ValueString())
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("id"), "Invalid service ID", err.Error())
			return
		}
		det, err := d.client.GetService(ctx, id)
		if err != nil {
			addAPIError(&resp.Diagnostics, "Unable to read Temps service "+formatID(id), err, perms)
			return
		}
		s = &det.Service
	} else {
		all, err := d.client.ListServices(ctx)
		if err != nil {
			addAPIError(&resp.Diagnostics, "Unable to list Temps services", err, perms)
			return
		}
		for i := range all {
			if all[i].Name == cfg.Name.ValueString() {
				s = &all[i]
				break
			}
		}
		if s == nil {
			resp.Diagnostics.AddError("Service not found", "No Temps service is named "+cfg.Name.String()+".")
			return
		}
	}
	cfg.ID = types.StringValue(formatID(s.ID))
	cfg.Name = types.StringValue(s.Name)
	cfg.ServiceType = types.StringValue(s.ServiceType)
	cfg.Version = types.StringPointerValue(s.Version)
	cfg.Topology = types.StringValue(s.Topology)
	cfg.Status = types.StringValue(s.Status)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// ---------------------------------------------------------- temps_service_types

type serviceTypesDataSource struct{ dataSourceBase }

func newServiceTypesDataSource() datasource.DataSource { return &serviceTypesDataSource{} }

type serviceTypesModel struct {
	ID    types.String `tfsdk:"id"`
	Types types.List   `tfsdk:"types"`
}

func (d *serviceTypesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_types"
}

func (d *serviceTypesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the service types the Temps server can run. Needs the `external_services:read` permission.",
		Attributes: map[string]schema.Attribute{
			"id":    schema.StringAttribute{Computed: true, MarkdownDescription: "Always `service_types`."},
			"types": schema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Service type names."},
		},
	}
}

func (d *serviceTypesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	ts, err := d.client.ListServiceTypes(ctx)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to list Temps service types", err, []string{"external_services:read"})
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &serviceTypesModel{ID: types.StringValue("service_types"), Types: stringListValue(ts)})...)
}

// ----------------------------------------------------- temps_service_connection

type serviceConnectionDataSource struct{ dataSourceBase }

func newServiceConnectionDataSource() datasource.DataSource { return &serviceConnectionDataSource{} }

type serviceConnectionModel struct {
	ID        types.String `tfsdk:"id"`
	ServiceID types.Int64  `tfsdk:"service_id"`
	ProjectID types.Int64  `tfsdk:"project_id"`
	Variables types.Map    `tfsdk:"variables"`
}

func (d *serviceConnectionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_connection"
}

func (d *serviceConnectionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The variables a linked service injects into a project (for PostgreSQL: `POSTGRES_URL`, `POSTGRES_HOST`, " +
			"`POSTGRES_PASSWORD`, ...). The values include credentials: they are sensitive and end up in the Terraform state. " +
			"Needs the `external_services:read` permission, and a role allowed to read plaintext service credentials.",
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true, MarkdownDescription: "`<service_id>/<project_id>`."},
			"service_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the service."},
			"project_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of a project linked to the service (see `temps_service_link`)."},
			"variables": schema.MapAttribute{
				Computed: true, Sensitive: true, ElementType: types.StringType,
				MarkdownDescription: "Variable name to value.",
			},
		},
	}
}

func (d *serviceConnectionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg serviceConnectionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	sid, pid := cfg.ServiceID.ValueInt64(), cfg.ProjectID.ValueInt64()
	perms := []string{"external_services:read"}
	names, err := d.client.ServiceVariableNames(ctx, sid, pid)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to list variables of Temps service "+formatID(sid)+" for project "+formatID(pid), err, perms)
		return
	}
	slices.Sort(names)
	vals := make(map[string]attr.Value, len(names))
	for _, n := range names {
		v, err := d.client.GetServiceVariable(ctx, sid, pid, n)
		if err != nil {
			addAPIError(&resp.Diagnostics, "Unable to read variable "+n+" of Temps service "+formatID(sid), err, perms)
			return
		}
		vals[n] = types.StringValue(v.Value)
	}
	cfg.ID = types.StringValue(formatID(sid) + "/" + formatID(pid))
	cfg.Variables = types.MapValueMust(types.StringType, vals)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// ----------------------------------------------------- temps_webhook_event_types

type webhookEventTypesDataSource struct{ dataSourceBase }

func newWebhookEventTypesDataSource() datasource.DataSource { return &webhookEventTypesDataSource{} }

type webhookEventTypesModel struct {
	ID         types.String `tfsdk:"id"`
	EventTypes types.List   `tfsdk:"event_types"`
	Categories types.Map    `tfsdk:"categories"`
}

func (d *webhookEventTypesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_webhook_event_types"
}

func (d *webhookEventTypesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the events a `temps_webhook` can subscribe to. Needs the `webhooks:read` permission.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Always `webhook_event_types`."},
			"event_types": schema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Event type names, for example `deployment.succeeded`."},
			"categories": schema.MapAttribute{Computed: true, ElementType: types.StringType,
				MarkdownDescription: "Event type name to its category, for example `Deployment`."},
		},
	}
}

func (d *webhookEventTypesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	ets, err := d.client.ListWebhookEventTypes(ctx)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to list Temps webhook event types", err, []string{"webhooks:read"})
		return
	}
	names := make([]string, len(ets))
	cats := map[string]attr.Value{}
	for i, e := range ets {
		names[i] = e.EventType
		cats[e.EventType] = types.StringValue(e.Category)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &webhookEventTypesModel{
		ID: types.StringValue("webhook_event_types"), EventTypes: stringListValue(names), Categories: types.MapValueMust(types.StringType, cats),
	})...)
}

// ---------------------------------------------------------- temps_project_group

type projectGroupDataSource struct{ dataSourceBase }

func newProjectGroupDataSource() datasource.DataSource { return &projectGroupDataSource{} }

type projectGroupDataModel struct {
	ID          types.String `tfsdk:"id"`
	Slug        types.String `tfsdk:"slug"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	ProjectIDs  types.Set    `tfsdk:"project_ids"`
}

func (d *projectGroupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_group"
}

func (d *projectGroupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a project group by `id` or `slug`, with the projects in it. Needs the `projects:read` permission.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Numeric project group ID."},
			"slug":        schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Project group slug."},
			"name":        schema.StringAttribute{Computed: true, MarkdownDescription: "Project group name."},
			"description": schema.StringAttribute{Computed: true, MarkdownDescription: "Description, null when none is set."},
			"project_ids": schema.SetAttribute{Computed: true, ElementType: types.Int64Type, MarkdownDescription: "IDs of the projects in the group."},
		},
	}
}

func (d *projectGroupDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("slug"))}
}

func (d *projectGroupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg projectGroupDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var g *client.ProjectGroup
	var err error
	if known(cfg.ID) {
		id, perr := parseID(cfg.ID.ValueString())
		if perr != nil {
			resp.Diagnostics.AddAttributeError(path.Root("id"), "Invalid project group ID", perr.Error())
			return
		}
		g, err = d.client.GetProjectGroup(ctx, id)
	} else {
		g, err = d.client.GetProjectGroupBySlug(ctx, cfg.Slug.ValueString())
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read Temps project group", err, []string{"projects:read"})
		return
	}
	ids := make([]int64, 0, len(g.Projects))
	for _, p := range g.Projects {
		ids = append(ids, p.ID)
	}
	slices.Sort(ids)
	cfg.ID = types.StringValue(formatID(g.ID))
	cfg.Slug = types.StringValue(g.Slug)
	cfg.Name = types.StringValue(g.Name)
	cfg.Description = types.StringNull()
	if g.Description != nil && *g.Description != "" {
		cfg.Description = types.StringValue(*g.Description)
	}
	cfg.ProjectIDs = int64SetValue(ids)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
