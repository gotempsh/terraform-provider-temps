// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

// temps_domain is served by the spec engine (domainSpec) plus its
// certificate settings, which live on their own endpoint.

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var (
	_ resource.Resource                = &domainResource{}
	_ resource.ResourceWithImportState = &domainResource{}
)

// defaultRenewalDays is what Temps uses for a domain without saved settings.
const defaultRenewalDays = 30

var certificateSettingsAttrTypes = map[string]attr.Type{
	"renewal_days": types.Int64Type, "alert_days": types.Int64Type, "alerts_enabled": types.BoolType,
}

type certificateSettingsModel struct {
	RenewalDays   types.Int64 `tfsdk:"renewal_days"`
	AlertDays     types.Int64 `tfsdk:"alert_days"`
	AlertsEnabled types.Bool  `tfsdk:"alerts_enabled"`
}

type domainResource struct{ genericResource }

func newDomainResource() resource.Resource { return &domainResource{genericResource{s: domainSpec}} }

func (r *domainResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	r.genericResource.Schema(ctx, req, resp)
	resp.Schema.MarkdownDescription += "\n\nCertificate settings need `domains:write` as well."
	days := []validator.Int64{int64validator.Between(1, 90)}
	resp.Schema.Attributes["certificate_settings"] = schema.SingleNestedAttribute{
		Optional: true, Computed: true,
		PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
		MarkdownDescription: "Renewal and expiry-alert policy of the certificate. Left out, Terraform does not manage it and " +
			"keeps what the domain has. Once set, a value you leave out returns to its default; " +
			"`certificate_settings = {}` resets them all. Saving it changes nothing about the current certificate: " +
			"the ACME challenge of future orders is kept as Temps has it. Read back from " +
			"`GET /domains/{id}/certificate-settings`.",
		Attributes: map[string]schema.Attribute{
			"renewal_days": schema.Int64Attribute{Optional: true, Computed: true, Validators: days,
				PlanModifiers: []planmodifier.Int64{settingOf{Default: types.Int64Value(defaultRenewalDays)}},
				MarkdownDescription: "Start ACME renewal this many days before expiry (1–90, default `30`). Imported " +
					"certificates are not renewed automatically whatever it says."},
			"alert_days": schema.Int64Attribute{Optional: true, Computed: true, Validators: days,
				PlanModifiers: []planmodifier.Int64{settingOf{}},
				MarkdownDescription: "Warn this many days before expiry (1–90) instead of the instance SSL expiry " +
					"threshold. Left out, the instance setting applies."},
			"alerts_enabled": schema.BoolAttribute{Optional: true, Computed: true,
				PlanModifiers:       []planmodifier.Bool{settingOf{}},
				MarkdownDescription: "Turn expiry alerts on or off for this domain. Left out, the instance setting applies."},
		},
	}
}

func certificateSettingsValue(s *client.DomainCertificateSettings) types.Object {
	return types.ObjectValueMust(certificateSettingsAttrTypes, map[string]attr.Value{
		"renewal_days": types.Int64Value(s.RenewalDays), "alert_days": types.Int64PointerValue(s.AlertDays),
		"alerts_enabled": types.BoolPointerValue(s.AlertsEnabled),
	})
}

func (r *domainResource) domainID(ctx context.Context, st tfsdk.State, diags *diag.Diagnostics) (int64, bool) {
	var id types.String
	diags.Append(st.GetAttribute(ctx, path.Root("id"), &id)...)
	if !known(id) {
		return 0, false
	}
	n, err := parseID(id.ValueString())
	if err != nil {
		diags.AddError("Invalid temps_domain ID", err.Error())
		return 0, false
	}
	return n, true
}

// writeCertificateSettings saves the planned settings when they differ from
// prior (nil when the domain has its defaults).
func (r *domainResource) writeCertificateSettings(ctx context.Context, id int64, planned, prior types.Object, diags *diag.Diagnostics) {
	want, ok := objectAs[certificateSettingsModel](ctx, planned, diags)
	if !ok {
		return
	}
	if have, ok := objectAs[certificateSettingsModel](ctx, prior, diags); ok &&
		have.RenewalDays.Equal(want.RenewalDays) && have.AlertDays.Equal(want.AlertDays) && have.AlertsEnabled.Equal(want.AlertsEnabled) {
		return
	}
	// PUT replaces the whole document: keep the challenge Temps has.
	current, err := r.client.GetDomainCertificateSettings(ctx, id)
	if err != nil {
		addAPIError(diags, "Unable to read the certificate settings of temps_domain "+formatID(id), err, r.s.Perms)
		return
	}
	renewal := int64(defaultRenewalDays)
	if known(want.RenewalDays) {
		renewal = want.RenewalDays.ValueInt64()
	}
	_, err = r.client.SetDomainCertificateSettings(ctx, id, client.DomainCertificateSettings{
		ChallengeType: current.ChallengeType, RenewalDays: renewal,
		AlertDays: int64Ptr(want.AlertDays), AlertsEnabled: boolPtr(want.AlertsEnabled),
	})
	if err != nil {
		addAPIError(diags, "Unable to save the certificate settings of temps_domain "+formatID(id), err,
			append(append([]string{}, r.s.Perms...), "domains:write"))
	}
}

// readCertificateSettings refreshes the settings in st. A server without the
// endpoint keeps what st holds (null when never known).
func (r *domainResource) readCertificateSettings(ctx context.Context, id int64, st *tfsdk.State, diags *diag.Diagnostics) {
	var cur types.Object
	diags.Append(st.GetAttribute(ctx, path.Root("certificate_settings"), &cur)...)
	s, err := r.client.GetDomainCertificateSettings(ctx, id)
	switch {
	case err == nil:
		cur = certificateSettingsValue(s)
	case client.IsUnsupported(err):
		if cur.IsUnknown() {
			cur = types.ObjectNull(certificateSettingsAttrTypes)
		}
	default:
		addAPIError(diags, "Unable to read the certificate settings of temps_domain "+formatID(id), err, r.s.Perms)
		return
	}
	diags.Append(st.SetAttribute(ctx, path.Root("certificate_settings"), cur)...)
}

func (r *domainResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	r.genericResource.Create(ctx, req, resp)
	id, ok := r.domainID(ctx, resp.State, &resp.Diagnostics)
	if !ok || resp.Diagnostics.HasError() {
		return
	}
	var planned types.Object
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("certificate_settings"), &planned)...)
	r.writeCertificateSettings(ctx, id, planned, types.ObjectNull(certificateSettingsAttrTypes), &resp.Diagnostics)
	var readDiags diag.Diagnostics
	r.readCertificateSettings(ctx, id, &resp.State, &readDiags)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(readDiags...)
	}
	var saved types.Object
	resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("certificate_settings"), &saved)...)
	if saved.IsUnknown() {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("certificate_settings"), types.ObjectNull(certificateSettingsAttrTypes))...)
	}
}

func (r *domainResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	r.genericResource.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
		return
	}
	if id, ok := r.domainID(ctx, resp.State, &resp.Diagnostics); ok {
		r.readCertificateSettings(ctx, id, &resp.State, &resp.Diagnostics)
	}
}

// Update only runs for certificate_settings: every other argument forces
// replacement.
func (r *domainResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	r.genericResource.Update(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		return
	}
	id, ok := r.domainID(ctx, resp.State, &resp.Diagnostics)
	if !ok {
		return
	}
	var planned, prior types.Object
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("certificate_settings"), &planned)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("certificate_settings"), &prior)...)
	r.writeCertificateSettings(ctx, id, planned, prior, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	r.readCertificateSettings(ctx, id, &resp.State, &resp.Diagnostics)
}
