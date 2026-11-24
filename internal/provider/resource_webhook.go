// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
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

var webhookPermissions = []string{"webhooks:read", "webhooks:create", "webhooks:write", "webhooks:delete"}

var (
	_ resource.Resource                = &webhookResource{}
	_ resource.ResourceWithImportState = &webhookResource{}
)

type webhookResource struct{ resourceBase }

func newWebhookResource() resource.Resource { return &webhookResource{} }

type webhookModel struct {
	ID        types.String `tfsdk:"id"`
	ProjectID types.Int64  `tfsdk:"project_id"`
	URL       types.String `tfsdk:"url"`
	Events    types.Set    `tfsdk:"events"`
	Enabled   types.Bool   `tfsdk:"enabled"`
	Secret    types.String `tfsdk:"secret"`
	HasSecret types.Bool   `tfsdk:"has_secret"`
}

// webhookURL rejects URLs the server refuses or would expose: only http(s),
// and no credentials (the API returns the URL in plain text to every reader).
type webhookURL struct{}

func (webhookURL) Description(context.Context) string {
	return "must be an absolute http or https URL without user:password@"
}
func (v webhookURL) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (webhookURL) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !known(req.ConfigValue) {
		return
	}
	u, err := url.Parse(req.ConfigValue.ValueString())
	var problem string
	switch {
	case err != nil:
		problem = err.Error()
	case u.Scheme != "http" && u.Scheme != "https":
		problem = "the scheme must be http or https"
	case u.Host == "":
		problem = "the URL needs a host"
	case u.User != nil:
		problem = "credentials in the URL are returned in plain text by the API; use secret to sign deliveries instead"
	}
	if problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid webhook URL", fmt.Sprintf("url %s: %s.", req.ConfigValue, problem))
	}
}

func (r *webhookResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_webhook"
}

func (r *webhookResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Delivers a project's events (deployments, domains and more) to an HTTP endpoint.\n\n" +
			"Temps resolves the host when the webhook is saved and refuses private, loopback and cloud metadata addresses " +
			"unless an administrator trusts that network. The `temps_webhook_event_types` data source lists valid events.\n\n" +
			"Import with `<project_id>/<webhook_id>`; `secret` cannot be read back, so set it again after importing.\n\n" +
			"Required API key permissions: " + permList(webhookPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric webhook ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the project.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"url": schema.StringAttribute{Required: true, MarkdownDescription: "Endpoint that receives a POST per event.",
				Validators: []validator.String{webhookURL{}}},
			"events": schema.SetAttribute{Required: true, ElementType: types.StringType,
				MarkdownDescription: "Event types to deliver, for example `deployment.succeeded`.",
				Validators:          []validator.Set{setvalidator.SizeAtLeast(1), setvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1))}},
			"enabled": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "Whether deliveries are sent. Defaults to `true`."},
			"secret": schema.StringAttribute{Optional: true, Sensitive: true,
				MarkdownDescription: "Key used to sign each delivery. Write-only on the server; the API cannot remove it once set, " +
					"so removing it replaces the webhook.",
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: []planmodifier.String{stringRequiresReplaceWhenRemoved("Temps cannot remove a webhook secret in place.")}},
			"has_secret": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the server holds a signing secret."},
		},
	}
}

func (m *webhookModel) fromAPI(w *client.Webhook) {
	m.ID = types.StringValue(formatID(w.ID))
	m.ProjectID = types.Int64Value(w.ProjectID)
	m.URL = types.StringValue(w.URL)
	vals := make([]string, 0, len(w.Events))
	seen := map[string]bool{}
	for _, e := range w.Events { // the server keeps duplicates; a set cannot
		if !seen[e] {
			seen[e] = true
			vals = append(vals, e)
		}
	}
	m.Events = stringSetValue(vals)
	m.Enabled = types.BoolValue(w.Enabled)
	m.HasSecret = types.BoolValue(w.HasSecret)
}

func (r *webhookResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan webhookModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	events := stringsFromSet(ctx, plan.Events, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	pid := plan.ProjectID.ValueInt64()
	w, err := r.client.CreateWebhook(ctx, pid, client.WebhookRequest{
		URL: strPtr(plan.URL), Events: &events, Enabled: boolPtr(plan.Enabled), Secret: strPtr(plan.Secret),
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create webhook for project "+formatID(pid), err, webhookPermissions)
		return
	}
	plan.fromAPI(w)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *webhookResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state webhookModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_webhook ID", err.Error())
		return
	}
	w, err := r.client.GetWebhook(ctx, state.ProjectID.ValueInt64(), id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read webhook "+formatID(id), err, webhookPermissions)
		return
	}
	state.fromAPI(w)
	// The secret is write-only: keep the configured value, but if the server
	// lost it (or it was removed outside Terraform) plan to set it again.
	if !w.HasSecret && !state.Secret.IsNull() {
		state.Secret = types.StringNull()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *webhookResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state webhookModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_webhook ID", err.Error())
		return
	}
	events := stringsFromSet(ctx, plan.Events, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	body := client.WebhookRequest{URL: strPtr(plan.URL), Events: &events, Enabled: boolPtr(plan.Enabled)}
	if !plan.Secret.Equal(state.Secret) {
		body.Secret = strPtr(plan.Secret)
	}
	w, err := r.client.UpdateWebhook(ctx, plan.ProjectID.ValueInt64(), id, body)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update webhook "+formatID(id), err, webhookPermissions)
		return
	}
	plan.fromAPI(w)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *webhookResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state webhookModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_webhook ID", err.Error())
		return
	}
	if err := r.client.DeleteWebhook(ctx, state.ProjectID.ValueInt64(), id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete webhook "+formatID(id), err, webhookPermissions)
	}
}

func (r *webhookResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	pid, id, err := parseCompositeID(req.ID, "<project_id>/<webhook_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), pid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), formatID(id))...)
}
