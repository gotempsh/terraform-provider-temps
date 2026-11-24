// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var notificationProviderPermissions = []string{
	"notification_providers:read", "notification_providers:create",
	"notification_providers:write", "notification_providers:delete",
}

var (
	_ resource.Resource                = (*notificationProviderResource)(nil)
	_ resource.ResourceWithImportState = (*notificationProviderResource)(nil)
)

type notificationProviderResource struct{ resourceBase }

func newNotificationProviderResource() resource.Resource { return &notificationProviderResource{} }

type notificationProviderModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Enabled     types.Bool   `tfsdk:"enabled"`
	URL         types.String `tfsdk:"url"`
	Method      types.String `tfsdk:"method"`
	Headers     types.Map    `tfsdk:"headers"`
	TimeoutSecs types.Int64  `tfsdk:"timeout_secs"`
}

func (r *notificationProviderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_notification_provider"
}

func (r *notificationProviderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A webhook notification provider. Temps posts alerts (failed deployments, " +
			"backups, monitors) to its URL. The server never returns the URL or header values, so " +
			"Terraform keeps them from configuration; after an import, set them in configuration again. " +
			"Required API key permissions: " + permList(notificationProviderPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Provider ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Display name.",
				Validators:  []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Whether notifications are sent. Defaults to `true`.",
			},
			"url": schema.StringAttribute{
				Required: true, Sensitive: true,
				Description: "http(s) URL notifications are sent to. Stored as a secret.",
				Validators:  []validator.String{webhookURL{}},
			},
			"method": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("POST"),
				Description: "HTTP method: `POST` (default), `PUT` or `PATCH`.",
				Validators:  []validator.String{stringvalidator.OneOf("POST", "PUT", "PATCH")},
			},
			"headers": schema.MapAttribute{
				Optional: true, Sensitive: true, ElementType: types.StringType,
				Description: "Extra request headers. Their values are stored as secrets.",
			},
			"timeout_secs": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(30),
				Description: "Request timeout in seconds, 1 to 120. Defaults to 30.",
				Validators:  []validator.Int64{int64validator.Between(1, 120)},
			},
		},
	}
}

func headersFromMap(ctx context.Context, m types.Map) (map[string]string, error) {
	out := map[string]string{}
	if !known(m) {
		return out, nil
	}
	vals := map[string]string{}
	if d := m.ElementsAs(ctx, &vals, false); d.HasError() {
		return nil, errFromDiags(d)
	}
	return vals, nil
}

func (m *notificationProviderModel) fromAPI(p *client.NotificationProvider) {
	m.ID = types.StringValue(formatID(p.ID))
	m.Name = types.StringValue(p.Name)
	m.Enabled = types.BoolValue(p.Enabled)
	if p.Config.Method != "" {
		m.Method = types.StringValue(p.Config.Method)
	}
	if p.Config.TimeoutSecs > 0 {
		m.TimeoutSecs = types.Int64Value(p.Config.TimeoutSecs)
	}
}

// reconcileHeaders keeps the values Terraform knows (the server masks them)
// for header names the server still has, adds names it has that Terraform
// does not know with the masked value, and drops names it no longer has.
func reconcileHeaders(prior map[string]string, server map[string]string) types.Map {
	if len(server) == 0 {
		if prior == nil {
			return types.MapNull(types.StringType)
		}
		return types.MapValueMust(types.StringType, map[string]attr.Value{})
	}
	vals := make(map[string]attr.Value, len(server))
	for k := range server {
		if v, ok := prior[k]; ok {
			vals[k] = types.StringValue(v)
		} else {
			vals[k] = types.StringValue(client.MaskedSecret)
		}
	}
	return types.MapValueMust(types.StringType, vals)
}

func (m *notificationProviderModel) request(ctx context.Context, prior *notificationProviderModel) (client.WebhookNotificationProviderRequest, error) {
	headers, err := headersFromMap(ctx, m.Headers)
	if err != nil {
		return client.WebhookNotificationProviderRequest{}, err
	}
	url := m.URL.ValueString()
	if prior != nil {
		priorHeaders, err := headersFromMap(ctx, prior.Headers)
		if err != nil {
			return client.WebhookNotificationProviderRequest{}, err
		}
		urlChanged := m.URL.ValueString() != prior.URL.ValueString()
		if !urlChanged {
			url = client.MaskedSecret
			// Unchanged header values are sent masked so the server keeps them.
			// When the URL changes the server refuses masked headers, so every
			// header is sent in plaintext.
			for k, v := range headers {
				if pv, ok := priorHeaders[k]; ok && pv == v {
					headers[k] = client.MaskedSecret
				}
			}
		}
	}
	return client.WebhookNotificationProviderRequest{
		Name:    m.Name.ValueString(),
		Enabled: m.Enabled.ValueBool(),
		Config: client.WebhookNotificationConfig{
			URL: url, Method: m.Method.ValueString(), Headers: headers,
			TimeoutSecs: m.TimeoutSecs.ValueInt64(),
		},
	}, nil
}

func (r *notificationProviderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan notificationProviderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := plan.request(ctx, nil)
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_notification_provider headers", err.Error())
		return
	}
	p, err := r.client.CreateWebhookNotificationProvider(ctx, body)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create notification provider "+plan.Name.ValueString(), err, notificationProviderPermissions)
		return
	}
	plan.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *notificationProviderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state notificationProviderModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_notification_provider ID", err.Error())
		return
	}
	p, err := r.client.GetNotificationProvider(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read notification provider "+state.ID.ValueString(), err, notificationProviderPermissions)
		return
	}
	if p.ProviderType != "" && p.ProviderType != "webhook" {
		resp.Diagnostics.AddError("Not a webhook notification provider",
			"Notification provider "+state.ID.ValueString()+" has type "+p.ProviderType+"; temps_notification_provider manages webhook providers only.")
		return
	}
	var knownHeaders map[string]string
	if known(state.Headers) {
		if knownHeaders, err = headersFromMap(ctx, state.Headers); err != nil {
			resp.Diagnostics.AddError("Invalid temps_notification_provider state", err.Error())
			return
		}
	}
	state.fromAPI(p)
	state.Headers = reconcileHeaders(knownHeaders, p.Config.Headers)
	if state.URL.IsNull() || state.URL.IsUnknown() {
		state.URL = types.StringValue(client.MaskedSecret)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *notificationProviderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state notificationProviderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_notification_provider ID", err.Error())
		return
	}
	prior := &state
	if state.URL.ValueString() == client.MaskedSecret {
		// Imported: Terraform never knew the stored secrets, so send them all.
		prior = nil
	}
	body, err := plan.request(ctx, prior)
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_notification_provider headers", err.Error())
		return
	}
	p, err := r.client.UpdateWebhookNotificationProvider(ctx, id, body)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update notification provider "+state.ID.ValueString(), err, notificationProviderPermissions)
		return
	}
	plan.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *notificationProviderResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state notificationProviderModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_notification_provider ID", err.Error())
		return
	}
	if err := r.client.DeleteNotificationProvider(ctx, id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete notification provider "+state.ID.ValueString(), err, notificationProviderPermissions)
	}
}

func (r *notificationProviderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := parseID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_notification_provider import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), formatID(id))...)
}
