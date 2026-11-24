// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var ipAccessPermissions = []string{"projects:read", "projects:write"}

const maxIPAccessCIDRs = 256

var (
	_ resource.Resource                   = &ipAccessResource{}
	_ resource.ResourceWithImportState    = &ipAccessResource{}
	_ resource.ResourceWithValidateConfig = &ipAccessResource{}
)

type ipAccessResource struct{ resourceBase }

func newIPAccessPolicyResource() resource.Resource { return &ipAccessResource{} }

type ipAccessModel struct {
	ID                 types.String `tfsdk:"id"`
	ProjectID          types.Int64  `tfsdk:"project_id"`
	EnvironmentID      types.Int64  `tfsdk:"environment_id"`
	Mode               types.String `tfsdk:"mode"`
	CIDRs              types.List   `tfsdk:"cidrs"`
	AcknowledgeLockout types.Bool   `tfsdk:"acknowledge_lockout"`
	Warnings           types.List   `tfsdk:"warnings"`
}

type ipAccessCIDRModel struct {
	CIDR        types.String `tfsdk:"cidr"`
	Description types.String `tfsdk:"description"`
}

var ipAccessCIDRType = types.ObjectType{AttrTypes: map[string]attr.Type{"cidr": types.StringType, "description": types.StringType}}

func (r *ipAccessResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ip_access_policy"
}

func (r *ipAccessResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Which client addresses may reach a project's apps through the Temps proxy, for the whole project " +
			"or, with `environment_id`, for one environment. An environment policy replaces the project policy for that environment; " +
			"it does not add to it. Destroying the resource deletes the policy, so an environment inherits the project policy again.\n\n" +
			"The policy filters traffic the proxy sees. It does not protect the Temps API or ports published outside the proxy, " +
			"and keeps application authentication necessary.\n\n" +
			"Temps refuses a policy that would block the address this Terraform run connects from unless `acknowledge_lockout` is `true`. " +
			"That address is the machine running Terraform, which may differ from the one people browse from.\n\n" +
			"Import with `<project_id>` or `<project_id>/<environment_id>`.\n\nRequired API key permissions: " + permList(ipAccessPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "`<project_id>` or `<project_id>/<environment_id>`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the project.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"environment_id": schema.Int64Attribute{Optional: true,
				MarkdownDescription: "ID of an environment of `project_id`. Omit it for the project-wide policy.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"mode": schema.StringAttribute{Required: true,
				MarkdownDescription: "`allow` admits only the listed clients, `deny` blocks them, and `disabled` keeps the list without enforcing it " +
					"(on an environment, `disabled` exempts it from the project policy).",
				Validators: []validator.String{stringvalidator.OneOf("allow", "deny", "disabled")}},
			"cidrs": schema.ListNestedAttribute{Optional: true, Computed: true,
				Default:             listdefault.StaticValue(types.ListValueMust(ipAccessCIDRType, []attr.Value{})),
				MarkdownDescription: fmt.Sprintf("Ordered list of networks, at most %d.", maxIPAccessCIDRs),
				Validators:          []validator.List{listvalidator.SizeAtMost(maxIPAccessCIDRs)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"cidr": schema.StringAttribute{Required: true,
						MarkdownDescription: "Network in canonical form: `10.0.0.0/8`, `203.0.113.7/32`, `2001:db8::/32`.",
						Validators:          []validator.String{canonicalCIDR{}}},
					"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Label, for example `Office`.",
						Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
				}},
			},
			"acknowledge_lockout": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Apply the policy even if it blocks the address Terraform connects from. Defaults to `false`."},
			"warnings": schema.ListAttribute{Computed: true, ElementType: types.StringType,
				MarkdownDescription: "What Temps says an operator should know about the saved policy, such as not being able to see real client addresses."},
		},
	}
}

func (r *ipAccessResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg ipAccessModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || !known(cfg.CIDRs) {
		if known(cfg.Mode) && cfg.Mode.ValueString() == "allow" && cfg.CIDRs.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("cidrs"), "Empty allow list",
				"An allow list with no entries would block every client; add at least one CIDR or set mode to \"disabled\".")
		}
		return
	}
	var entries []ipAccessCIDRModel
	resp.Diagnostics.Append(cfg.CIDRs.ElementsAs(ctx, &entries, false)...)
	if known(cfg.Mode) && cfg.Mode.ValueString() == "allow" && len(entries) == 0 {
		resp.Diagnostics.AddAttributeError(path.Root("cidrs"), "Empty allow list",
			"An allow list with no entries would block every client; add at least one CIDR or set mode to \"disabled\".")
	}
	seen := map[string]int{}
	for i, e := range entries {
		if !known(e.CIDR) {
			continue
		}
		c := strings.ToLower(e.CIDR.ValueString())
		if j, dup := seen[c]; dup {
			resp.Diagnostics.AddAttributeError(path.Root("cidrs").AtListIndex(i).AtName("cidr"), "Duplicate CIDR",
				fmt.Sprintf("%s is already listed at index %d.", e.CIDR.ValueString(), j))
		}
		seen[c] = i
	}
}

func (m *ipAccessModel) fromAPI(p *client.IPAccessPolicy) {
	if p.EnvironmentID != nil {
		m.ID = types.StringValue(fmt.Sprintf("%d/%d", p.ProjectID, *p.EnvironmentID))
	} else {
		m.ID = types.StringValue(formatID(p.ProjectID))
	}
	m.ProjectID = types.Int64Value(p.ProjectID)
	m.EnvironmentID = types.Int64PointerValue(p.EnvironmentID)
	m.Mode = types.StringValue(p.Mode)
	elems := make([]attr.Value, len(p.CIDRs))
	for i, c := range p.CIDRs {
		elems[i] = types.ObjectValueMust(ipAccessCIDRType.AttrTypes, map[string]attr.Value{
			"cidr": types.StringValue(c.CIDR), "description": types.StringPointerValue(c.Description),
		})
	}
	m.CIDRs = types.ListValueMust(ipAccessCIDRType, elems)
	m.Warnings = stringListValue(p.Warnings)
	if m.AcknowledgeLockout.IsNull() || m.AcknowledgeLockout.IsUnknown() {
		m.AcknowledgeLockout = types.BoolValue(false)
	}
}

func (m *ipAccessModel) scope() string {
	if known(m.EnvironmentID) {
		return fmt.Sprintf("environment %d of project %d", m.EnvironmentID.ValueInt64(), m.ProjectID.ValueInt64())
	}
	return "project " + formatID(m.ProjectID.ValueInt64())
}

func (r *ipAccessResource) put(ctx context.Context, plan *ipAccessModel, diags *diag.Diagnostics, summary string) {
	var entries []ipAccessCIDRModel
	diags.Append(plan.CIDRs.ElementsAs(ctx, &entries, false)...)
	if diags.HasError() {
		return
	}
	body := client.SetIPAccessPolicyRequest{Mode: plan.Mode.ValueString(), AcknowledgeLockout: plan.AcknowledgeLockout.ValueBool()}
	for _, e := range entries {
		body.CIDRs = append(body.CIDRs, client.IPAccessCIDR{CIDR: e.CIDR.ValueString(), Description: strPtr(e.Description)})
	}
	p, err := r.client.SetIPAccessPolicy(ctx, plan.ProjectID.ValueInt64(), plan.EnvironmentID.ValueInt64(), body)
	if client.IsConflict(err) {
		err = withHint{err, "If blocking that address is intended (it is the machine running Terraform), set acknowledge_lockout = true."}
	}
	if err != nil {
		addAPIError(diags, summary+" "+plan.scope(), err, ipAccessPermissions)
		return
	}
	plan.fromAPI(p)
}

func (r *ipAccessResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ipAccessModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// PUT replaces the whole policy: refuse to silently overwrite one that
	// was configured outside Terraform.
	existing, err := r.client.GetIPAccessPolicy(ctx, plan.ProjectID.ValueInt64(), plan.EnvironmentID.ValueInt64())
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the IP access policy of "+plan.scope(), err, ipAccessPermissions)
		return
	}
	if existing.Configured {
		id := formatID(plan.ProjectID.ValueInt64())
		if known(plan.EnvironmentID) {
			id += "/" + formatID(plan.EnvironmentID.ValueInt64())
		}
		resp.Diagnostics.AddError("IP access policy already exists",
			fmt.Sprintf("%s already has an IP access policy (mode %q, %d CIDRs). Import it to manage it with Terraform: terraform import <address> %s",
				plan.scope(), existing.Mode, len(existing.CIDRs), id))
		return
	}
	r.put(ctx, &plan, &resp.Diagnostics, "Unable to set the IP access policy of")
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ipAccessResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ipAccessModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.client.GetIPAccessPolicy(ctx, state.ProjectID.ValueInt64(), state.EnvironmentID.ValueInt64())
	if client.IsNotFound(err) || (err == nil && !p.Configured) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the IP access policy of "+state.scope(), err, ipAccessPermissions)
		return
	}
	state.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ipAccessResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ipAccessModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.put(ctx, &plan, &resp.Diagnostics, "Unable to update the IP access policy of")
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ipAccessResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ipAccessModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteIPAccessPolicy(ctx, state.ProjectID.ValueInt64(), state.EnvironmentID.ValueInt64())
	if err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete the IP access policy of "+state.scope(), err, ipAccessPermissions)
	}
}

func (r *ipAccessResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	var pid, env int64
	var err error
	if strings.Contains(req.ID, "/") {
		pid, env, err = parseCompositeID(req.ID, "<project_id> or <project_id>/<environment_id>")
	} else {
		pid, err = parseID(req.ID)
	}
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), pid)...)
	if env != 0 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("environment_id"), env)...)
	}
}
