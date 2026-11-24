// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
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

var statusPageDomainPermissions = []string{"status_page:read", "status_page:write"}

var (
	_ resource.Resource                = &statusPageDomainResource{}
	_ resource.ResourceWithImportState = &statusPageDomainResource{}
)

type statusPageDomainResource struct{ resourceBase }

func newStatusPageDomainResource() resource.Resource { return &statusPageDomainResource{} }

type statusPageDomainModel struct {
	ID                 types.String `tfsdk:"id"`
	StatusPageID       types.Int64  `tfsdk:"status_page_id"`
	Hostname           types.String `tfsdk:"hostname"`
	VerificationMethod types.String `tfsdk:"verification_method"`
	VerifyOnApply      types.Bool   `tfsdk:"verify_on_apply"`
	Status             types.String `tfsdk:"status"`
	Serving            types.Bool   `tfsdk:"serving"`
	LastError          types.String `tfsdk:"last_error"`
	DNSRecords         types.List   `tfsdk:"dns_records"`
}

var dnsRecordAttrTypes = map[string]attr.Type{
	"record_type": types.StringType, "name": types.StringType, "value": types.StringType, "purpose": types.StringType,
}

func (r *statusPageDomainResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_status_page_domain"
}

func (r *statusPageDomainResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A custom hostname for a status page. Create the records in `dns_records` at your DNS provider; " +
			"Temps verifies them and issues a certificate. `status` reports progress (`pending`, `active`, `failed`, ...).\n\n" +
			"Import with `<status_page_id>/<domain_id>`.\n\nRequired API key permissions: " + permList(statusPageDomainPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric domain ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"status_page_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the status page.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"hostname": schema.StringAttribute{Required: true, MarkdownDescription: "Hostname, for example `status.example.com`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.LengthBetween(3, 253)}},
			"verification_method": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "`http-01` (default) or `dns-01`.",
				Validators:          []validator.String{stringvalidator.OneOf("http-01", "dns-01")},
			},
			"verify_on_apply": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Ask Temps to verify the DNS records right after creating the domain. A failed check is reported in `status` and `last_error`, not as an apply error.",
			},
			"status":     schema.StringAttribute{Computed: true, MarkdownDescription: "Verification and certificate status."},
			"serving":    schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the hostname is serving the page."},
			"last_error": schema.StringAttribute{Computed: true, MarkdownDescription: "Last verification or certificate error."},
			"dns_records": schema.ListNestedAttribute{
				Computed: true, MarkdownDescription: "DNS records to create.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"record_type": schema.StringAttribute{Computed: true, MarkdownDescription: "Record type, for example `CNAME` or `TXT`."},
					"name":        schema.StringAttribute{Computed: true, MarkdownDescription: "Record name."},
					"value":       schema.StringAttribute{Computed: true, MarkdownDescription: "Record value."},
					"purpose":     schema.StringAttribute{Computed: true, MarkdownDescription: "What the record is for."},
				}},
			},
		},
	}
}

func (m *statusPageDomainModel) fromAPI(d *client.StatusPageDomain, pageID int64) {
	m.ID = types.StringValue(formatID(d.ID))
	m.StatusPageID = types.Int64Value(pageID)
	m.Hostname = types.StringValue(d.Hostname)
	m.VerificationMethod = types.StringValue(d.VerificationMethod)
	m.Status = types.StringValue(d.Status)
	m.Serving = types.BoolValue(d.Serving)
	m.LastError = types.StringPointerValue(d.LastError)
	recs := make([]attr.Value, len(d.DNSRecords))
	for i, rec := range d.DNSRecords {
		recs[i] = types.ObjectValueMust(dnsRecordAttrTypes, map[string]attr.Value{
			"record_type": types.StringValue(rec.RecordType), "name": types.StringValue(rec.Name),
			"value": types.StringValue(rec.Value), "purpose": types.StringValue(rec.Purpose),
		})
	}
	m.DNSRecords = types.ListValueMust(types.ObjectType{AttrTypes: dnsRecordAttrTypes}, recs)
}

func (r *statusPageDomainResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan statusPageDomainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pageID := plan.StatusPageID.ValueInt64()
	d, err := r.client.AddStatusPageDomain(ctx, pageID, plan.Hostname.ValueString(), strPtr(plan.VerificationMethod))
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to add domain "+plan.Hostname.String()+" to Temps status page "+formatID(pageID), err, statusPageDomainPermissions)
		return
	}
	if plan.VerifyOnApply.ValueBool() {
		if v, err := r.client.VerifyStatusPageDomain(ctx, pageID, d.ID); err == nil {
			d = v
		} else {
			resp.Diagnostics.AddWarning("Domain verification request failed", err.Error())
		}
	}
	plan.fromAPI(d, pageID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *statusPageDomainResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state statusPageDomainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pageID := state.StatusPageID.ValueInt64()
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_status_page_domain ID", err.Error())
		return
	}
	d, err := r.client.GetStatusPageDomain(ctx, pageID, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read domain "+formatID(id)+" of Temps status page "+formatID(pageID), err, statusPageDomainPermissions)
		return
	}
	state.fromAPI(d, pageID)
	if state.VerifyOnApply.IsNull() {
		state.VerifyOnApply = types.BoolValue(false)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update only changes verify_on_apply, which affects nothing until the next create.
func (r *statusPageDomainResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state statusPageDomainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.VerifyOnApply = plan.VerifyOnApply
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *statusPageDomainResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state statusPageDomainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pageID := state.StatusPageID.ValueInt64()
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_status_page_domain ID", err.Error())
		return
	}
	if err := r.client.DeleteStatusPageDomain(ctx, pageID, id); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to remove domain "+formatID(id)+" from Temps status page "+formatID(pageID), err, statusPageDomainPermissions)
	}
}

func (r *statusPageDomainResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	pageID, id, err := parseCompositeID(req.ID, "<status_page_id>/<domain_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("status_page_id"), pageID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), formatID(id))...)
}
