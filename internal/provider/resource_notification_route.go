// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"fmt"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var notificationRoutePermissions = []string{"notification_providers:read", "notification_providers:create",
	"notification_providers:write", "notification_providers:delete"}

// alertSeverities are the severities a route covers, lowest first.
var alertSeverities = []string{"debug", "info", "warning", "error", "critical", "emergency"}

var (
	_ resource.Resource                   = &notificationRouteResource{}
	_ resource.ResourceWithImportState    = &notificationRouteResource{}
	_ resource.ResourceWithValidateConfig = &notificationRouteResource{}
)

type notificationRouteResource struct{ resourceBase }

func newNotificationRouteResource() resource.Resource { return &notificationRouteResource{} }

type notificationRouteModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Enabled     types.Bool   `tfsdk:"enabled"`
	MinSeverity types.String `tfsdk:"min_severity"`
	MaxSeverity types.String `tfsdk:"max_severity"`
	ProviderIDs types.Set    `tfsdk:"provider_ids"`
}

func (r *notificationRouteResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_notification_route"
}

func (r *notificationRouteResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	severity := []validator.String{stringvalidator.OneOf(alertSeverities...)}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sends alerts within a severity range to notification providers.\n\n" +
			"Deleting a notification provider outside Terraform leaves the route with no providers; the next plan adds " +
			"the configured ones back.\n\n" +
			"Import with the route ID.\n\n" +
			"Required API key permissions: " + permList(notificationRoutePermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric route ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Unique route name, up to 255 characters.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 255), notBlank{}}},
			"enabled": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "Whether the route sends alerts. Defaults to `true`."},
			"min_severity": schema.StringAttribute{Required: true, Validators: severity,
				MarkdownDescription: "Lowest severity the route sends: one of `" + joinTicks(alertSeverities) + "`."},
			"max_severity": schema.StringAttribute{Required: true, Validators: severity,
				MarkdownDescription: "Highest severity the route sends; not lower than `min_severity`."},
			"provider_ids": schema.SetAttribute{Required: true, ElementType: types.Int64Type,
				MarkdownDescription: "IDs of the notification providers the alerts go to.",
				Validators:          []validator.Set{setvalidator.SizeAtLeast(1)}},
		},
	}
}

func (r *notificationRouteResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg notificationRouteModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || !known(cfg.MinSeverity) || !known(cfg.MaxSeverity) {
		return
	}
	lo := slices.Index(alertSeverities, cfg.MinSeverity.ValueString())
	hi := slices.Index(alertSeverities, cfg.MaxSeverity.ValueString())
	if lo >= 0 && hi >= 0 && lo > hi {
		resp.Diagnostics.AddAttributeError(path.Root("min_severity"), "Invalid severity range",
			fmt.Sprintf("min_severity %q is higher than max_severity %q.", cfg.MinSeverity.ValueString(), cfg.MaxSeverity.ValueString()))
	}
}

func (m *notificationRouteModel) fromAPI(r *client.NotificationRoute) {
	m.ID = types.StringValue(formatID(r.ID))
	m.Name = types.StringValue(r.Name)
	m.Enabled = types.BoolValue(r.Enabled)
	m.MinSeverity = types.StringValue(r.MinSeverity)
	m.MaxSeverity = types.StringValue(r.MaxSeverity)
	m.ProviderIDs = int64SetValue(r.ProviderIDs)
}

func routeRequest(ctx context.Context, m *notificationRouteModel, d *diag.Diagnostics) client.NotificationRouteRequest {
	return client.NotificationRouteRequest{
		Name: m.Name.ValueString(), Enabled: m.Enabled.ValueBool(),
		MinSeverity: m.MinSeverity.ValueString(), MaxSeverity: m.MaxSeverity.ValueString(),
		ProviderIDs: int64sFromSet(ctx, m.ProviderIDs, d),
	}
}

func (r *notificationRouteResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan notificationRouteModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := routeRequest(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	rt, err := r.client.CreateNotificationRoute(ctx, body)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create notification route "+plan.Name.ValueString(), err, notificationRoutePermissions)
		return
	}
	plan.fromAPI(rt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *notificationRouteResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state notificationRouteModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_notification_route ID", err.Error())
		return
	}
	rt, err := r.client.GetNotificationRoute(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read notification route "+formatID(id), err, notificationRoutePermissions)
		return
	}
	state.fromAPI(rt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *notificationRouteResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state notificationRouteModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_notification_route ID", err.Error())
		return
	}
	body := routeRequest(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	rt, err := r.client.UpdateNotificationRoute(ctx, id, body)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update notification route "+formatID(id), err, notificationRoutePermissions)
		return
	}
	plan.fromAPI(rt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *notificationRouteResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state notificationRouteModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_notification_route ID", err.Error())
		return
	}
	if err := r.client.DeleteNotificationRoute(ctx, id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete notification route "+formatID(id), err, notificationRoutePermissions)
	}
}

func (r *notificationRouteResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
