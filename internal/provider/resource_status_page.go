// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var statusPagePermissions = []string{"status_page:create", "status_page:read", "status_page:write", "status_page:delete"}

var (
	_ resource.Resource                = &statusPageResource{}
	_ resource.ResourceWithImportState = &statusPageResource{}
)

type statusPageResource struct{ resourceBase }

func newStatusPageResource() resource.Resource { return &statusPageResource{} }

type statusPageModel struct {
	ID                   types.String             `tfsdk:"id"`
	Title                types.String             `tfsdk:"title"`
	Description          types.String             `tfsdk:"description"`
	Monitors             []statusPageMonitorModel `tfsdk:"monitors"`
	Appearance           types.Object             `tfsdk:"appearance"`
	Published            types.Bool               `tfsdk:"published"`
	PasswordWO           types.String             `tfsdk:"password_wo"`
	PasswordWOVersion    types.Int64              `tfsdk:"password_wo_version"`
	SubscriptionsEnabled types.Bool               `tfsdk:"subscriptions_enabled"`
	SubscriptionSender   types.String             `tfsdk:"subscription_sender"`
	Slug                 types.String             `tfsdk:"slug"`
	PublicURL            types.String             `tfsdk:"public_url"`
	PublicJSONURL        types.String             `tfsdk:"public_json_url"`
}

type statusPageMonitorModel struct {
	MonitorID   types.Int64  `tfsdk:"monitor_id"`
	DisplayName types.String `tfsdk:"display_name"`
	GroupName   types.String `tfsdk:"group_name"`
}

type appearanceModel struct {
	Theme                types.String `tfsdk:"theme"`
	AccentColor          types.String `tfsdk:"accent_color"`
	LogoURL              types.String `tfsdk:"logo_url"`
	WebsiteURL           types.String `tfsdk:"website_url"`
	ShowUptimePercentage types.Bool   `tfsdk:"show_uptime_percentage"`
	ShowResponseTimes    types.Bool   `tfsdk:"show_response_times"`
}

var appearanceAttrTypes = map[string]attr.Type{
	"theme": types.StringType, "accent_color": types.StringType, "logo_url": types.StringType,
	"website_url": types.StringType, "show_uptime_percentage": types.BoolType, "show_response_times": types.BoolType,
}

func (r *statusPageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_status_page"
}

func (r *statusPageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keepStr := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	keepBool := []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A public status page showing the state of monitors.\n\n" +
			"`public_url` stays empty until the Temps server has a public URL configured (Settings > Public ingress); " +
			"the page is also served at `<temps>/api/status/<slug>`.\n\n" +
			"`password_wo` and the subscription arguments need a Temps server that provides the status page password and " +
			"subscription endpoints; on older servers the apply fails with a diagnostic that says so.\n\n" +
			"Required API key permissions: " + permList(statusPagePermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id":    schema.StringAttribute{Computed: true, PlanModifiers: keepStr, MarkdownDescription: "Numeric status page ID."},
			"title": schema.StringAttribute{Required: true, MarkdownDescription: "Page title.", Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"description": schema.StringAttribute{
				Optional: true, MarkdownDescription: "Text shown under the title.",
			},
			"monitors": schema.ListNestedAttribute{
				Required:            true,
				MarkdownDescription: "Monitors shown on the page, in display order. Temps requires at least one.",
				Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"monitor_id":   schema.Int64Attribute{Required: true, MarkdownDescription: "ID of a `temps_monitor`."},
					"display_name": schema.StringAttribute{Required: true, MarkdownDescription: "Name shown on the page."},
					"group_name":   schema.StringAttribute{Optional: true, MarkdownDescription: "Group heading the monitor is listed under."},
				}},
			},
			"appearance": schema.SingleNestedAttribute{
				Optional: true, Computed: true,
				PlanModifiers:       []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "How the page looks. Unset fields keep the server defaults.",
				Attributes: map[string]schema.Attribute{
					"theme": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keepStr,
						MarkdownDescription: "`auto` (default), `light` or `dark`.",
						Validators:          []validator.String{stringvalidator.OneOf("auto", "light", "dark")}},
					"accent_color":           schema.StringAttribute{Optional: true, MarkdownDescription: "Accent colour, for example `#2563eb`."},
					"logo_url":               schema.StringAttribute{Optional: true, MarkdownDescription: "URL of a logo image."},
					"website_url":            schema.StringAttribute{Optional: true, MarkdownDescription: "Link back to your website."},
					"show_uptime_percentage": schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: keepBool, MarkdownDescription: "Show uptime percentages."},
					"show_response_times":    schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: keepBool, MarkdownDescription: "Show response times."},
				},
			},
			"published": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Whether the page is publicly reachable.",
			},
			"password_wo": schema.StringAttribute{
				Optional: true, Sensitive: true, WriteOnly: true,
				MarkdownDescription: "Write-only password protecting the page (Terraform 1.11+). Bump `password_wo_version` to change it; " +
					"remove both to remove the password.",
			},
			"password_wo_version": schema.Int64Attribute{
				Optional: true, MarkdownDescription: "Change this number to send a new `password_wo`.",
				Validators: []validator.Int64{int64validator.AlsoRequires(path.MatchRoot("password_wo"))},
			},
			"subscriptions_enabled": schema.BoolAttribute{
				Optional: true, MarkdownDescription: "Let visitors subscribe to email updates.",
			},
			"subscription_sender": schema.StringAttribute{
				Optional: true, MarkdownDescription: "Sender address for subscription emails, on a verified email domain.",
			},
			"slug":            schema.StringAttribute{Computed: true, PlanModifiers: keepStr, MarkdownDescription: "Unguessable slug in the page's public path."},
			"public_url":      schema.StringAttribute{Computed: true, MarkdownDescription: "Public URL, when the server has a public URL configured."},
			"public_json_url": schema.StringAttribute{Computed: true, MarkdownDescription: "Public JSON summary URL, when the server has a public URL configured."},
		},
	}
}

func (m *statusPageModel) monitorsRequest() *[]client.StatusPageMonitor {
	out := make([]client.StatusPageMonitor, len(m.Monitors))
	for i, mon := range m.Monitors {
		out[i] = client.StatusPageMonitor{MonitorID: mon.MonitorID.ValueInt64(), DisplayName: mon.DisplayName.ValueString(), GroupName: strPtr(mon.GroupName)}
	}
	return &out
}

func (m *statusPageModel) appearanceRequest(ctx context.Context, diags *diag.Diagnostics) *client.StatusPageAppearance {
	if !known(m.Appearance) {
		return nil
	}
	var a appearanceModel
	diags.Append(m.Appearance.As(ctx, &a, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
	return &client.StatusPageAppearance{
		Theme: strPtr(a.Theme), AccentColor: strPtr(a.AccentColor), LogoURL: strPtr(a.LogoURL), WebsiteURL: strPtr(a.WebsiteURL),
		ShowUptimePercentage: boolPtr(a.ShowUptimePercentage), ShowResponseTimes: boolPtr(a.ShowResponseTimes),
	}
}

func (m *statusPageModel) fromAPI(p *client.StatusPage) {
	m.ID = types.StringValue(formatID(p.ID))
	m.Title = types.StringValue(p.Title)
	m.Description = types.StringPointerValue(p.Description)
	if p.Description != nil && *p.Description == "" {
		m.Description = types.StringNull()
	}
	mons := make([]statusPageMonitorModel, len(p.Monitors))
	for i, mon := range p.Monitors {
		mons[i] = statusPageMonitorModel{
			MonitorID: types.Int64Value(mon.MonitorID), DisplayName: types.StringValue(mon.DisplayName), GroupName: types.StringPointerValue(mon.GroupName),
		}
	}
	m.Monitors = mons
	a := p.Appearance
	theme := "auto"
	if a.Theme != nil {
		theme = *a.Theme
	}
	m.Appearance = types.ObjectValueMust(appearanceAttrTypes, map[string]attr.Value{
		"theme": types.StringValue(theme), "accent_color": types.StringPointerValue(a.AccentColor),
		"logo_url": types.StringPointerValue(a.LogoURL), "website_url": types.StringPointerValue(a.WebsiteURL),
		"show_uptime_percentage": types.BoolValue(a.ShowUptimePercentage != nil && *a.ShowUptimePercentage),
		"show_response_times":    types.BoolValue(a.ShowResponseTimes != nil && *a.ShowResponseTimes),
	})
	m.Published = types.BoolValue(p.IsPublished)
	m.Slug = types.StringValue(p.Slug)
	m.PublicURL = types.StringPointerValue(p.PublicURL)
	m.PublicJSONURL = types.StringPointerValue(p.PublicJSONURL)
	m.PasswordWO = types.StringNull()
}

// applyExtras sets the publish state, password and subscription settings.
func (r *statusPageResource) applyExtras(ctx context.Context, id int64, plan, state, cfg *statusPageModel, current *client.StatusPage, diags *diag.Diagnostics) {
	if plan.Published.ValueBool() != current.IsPublished {
		if _, err := r.client.SetStatusPagePublished(ctx, id, plan.Published.ValueBool()); err != nil {
			addAPIError(diags, "Unable to change the published state of Temps status page "+formatID(id), err, statusPagePermissions)
			return
		}
	}
	switch {
	case known(cfg.PasswordWO) && (state == nil || !plan.PasswordWOVersion.Equal(state.PasswordWOVersion) || state.PasswordWOVersion.IsNull()):
		if err := r.client.SetStatusPagePassword(ctx, id, cfg.PasswordWO.ValueString()); err != nil {
			addAPIError(diags, "Unable to set the password of Temps status page "+formatID(id), err, statusPagePermissions)
			return
		}
	case state != nil && cfg.PasswordWO.IsNull() && !state.PasswordWOVersion.IsNull():
		if err := r.client.SetStatusPagePassword(ctx, id, ""); err != nil {
			addAPIError(diags, "Unable to remove the password of Temps status page "+formatID(id), err, statusPagePermissions)
			return
		}
	}
	subsChanged := state == nil || !plan.SubscriptionsEnabled.Equal(state.SubscriptionsEnabled) || !plan.SubscriptionSender.Equal(state.SubscriptionSender)
	if subsChanged && (known(plan.SubscriptionsEnabled) || known(plan.SubscriptionSender)) {
		_, err := r.client.UpdateStatusPageSubscriptions(ctx, id, client.StatusPageSubscriptions{
			Enabled: plan.SubscriptionsEnabled.ValueBool(), Sender: strPtr(plan.SubscriptionSender),
		})
		if err != nil {
			addAPIError(diags, "Unable to update subscription settings of Temps status page "+formatID(id), err, statusPagePermissions)
		}
	}
}

func (r *statusPageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg statusPageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := client.StatusPageRequest{
		Title: strPtr(plan.Title), Description: strPtr(plan.Description),
		Monitors: plan.monitorsRequest(), Appearance: plan.appearanceRequest(ctx, &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.client.CreateStatusPage(ctx, body)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create Temps status page "+plan.Title.String(), err, statusPagePermissions)
		return
	}
	r.applyExtras(ctx, p.ID, &plan, nil, &cfg, p, &resp.Diagnostics)
	final, err := r.client.GetStatusPage(ctx, p.ID)
	if err != nil {
		final = p
	}
	result := plan
	result.fromAPI(final)
	resp.Diagnostics.Append(resp.State.Set(ctx, &result)...)
}

func (r *statusPageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state statusPageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_status_page ID", err.Error())
		return
	}
	p, err := r.client.GetStatusPage(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read Temps status page "+formatID(id), err, statusPagePermissions)
		return
	}
	state.fromAPI(p)
	if !state.SubscriptionsEnabled.IsNull() || !state.SubscriptionSender.IsNull() {
		subs, err := r.client.GetStatusPageSubscriptions(ctx, id)
		switch {
		case err == nil:
			if !state.SubscriptionsEnabled.IsNull() {
				state.SubscriptionsEnabled = types.BoolValue(subs.Enabled)
			}
			if !state.SubscriptionSender.IsNull() {
				state.SubscriptionSender = types.StringPointerValue(subs.Sender)
			}
		case client.IsUnsupported(err):
			// Server without the endpoint: keep the configured values.
		default:
			addAPIError(&resp.Diagnostics, "Unable to read subscription settings of Temps status page "+formatID(id), err, statusPagePermissions)
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *statusPageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, cfg statusPageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_status_page ID", err.Error())
		return
	}
	desc := plan.Description.ValueString()
	body := client.StatusPageRequest{
		Title: strPtr(plan.Title), Description: &desc,
		Monitors: plan.monitorsRequest(), Appearance: plan.appearanceRequest(ctx, &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.client.UpdateStatusPage(ctx, id, body)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update Temps status page "+formatID(id), err, statusPagePermissions)
		return
	}
	r.applyExtras(ctx, id, &plan, &state, &cfg, p, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	final, err := r.client.GetStatusPage(ctx, id)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read Temps status page "+formatID(id)+" after update", err, statusPagePermissions)
		return
	}
	result := plan
	result.fromAPI(final)
	resp.Diagnostics.Append(resp.State.Set(ctx, &result)...)
}

func (r *statusPageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state statusPageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_status_page ID", err.Error())
		return
	}
	if err := r.client.DeleteStatusPage(ctx, id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete Temps status page "+formatID(id), err, statusPagePermissions)
	}
}

func (r *statusPageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := parseID(req.ID); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
