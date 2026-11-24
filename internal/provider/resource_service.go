// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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

var servicePermissions = []string{"external_services:create", "external_services:read", "external_services:write", "external_services:delete"}

var serviceTypes = []string{"postgres", "redis", "mongodb", "mariadb", "s3", "rustfs", "kv", "blob"}

const defaultServiceTimeout = 15 * time.Minute

var (
	_ resource.Resource                = &serviceResource{}
	_ resource.ResourceWithImportState = &serviceResource{}
	_ resource.ResourceWithModifyPlan  = &serviceResource{}
)

type serviceResource struct{ resourceBase }

func newServiceResource() resource.Resource { return &serviceResource{} }

type serviceModel struct {
	ID                 types.String   `tfsdk:"id"`
	Name               types.String   `tfsdk:"name"`
	ServiceType        types.String   `tfsdk:"service_type"`
	Version            types.String   `tfsdk:"version"`
	DockerImage        types.String   `tfsdk:"docker_image"`
	Topology           types.String   `tfsdk:"topology"`
	NodeID             types.Int64    `tfsdk:"node_id"`
	Parameters         types.Map      `tfsdk:"parameters"`
	DeletionProtection types.Bool     `tfsdk:"deletion_protection"`
	Status             types.String   `tfsdk:"status"`
	Timeouts           timeouts.Value `tfsdk:"timeouts"`
}

func (r *serviceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service"
}

func (r *serviceResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A managed service that Temps runs and operates: PostgreSQL, Redis, MongoDB, MariaDB or an " +
			"S3-compatible object store. Link it to projects with `temps_service_link` to inject its connection variables.\n\n" +
			"Creation is asynchronous; the provider waits until the service is `running` (see `timeouts`). " +
			"`deletion_protection` defaults to `true`: set it to `false` and apply before destroying, because destroying a service deletes its data.\n\n" +
			"Required API key permissions: " + permList(servicePermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric service ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name": schema.StringAttribute{
				Required: true, PlanModifiers: replace,
				MarkdownDescription: "Service name; also names its container. Changing it replaces the service.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 63)},
			},
			"service_type": schema.StringAttribute{
				Required: true, PlanModifiers: replace,
				MarkdownDescription: "One of `postgres`, `redis`, `mongodb`, `mariadb`, `s3`, `rustfs`, `kv`, `blob` " +
					"(the server's list is in the `temps_service_types` data source). Changing it replaces the service.",
				Validators: []validator.String{stringvalidator.OneOf(serviceTypes...)},
			},
			"version": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "Engine version chosen at creation. The API cannot change it in place, so changing it " +
					"replaces the service; to upgrade in place change `docker_image` instead.",
			},
			"docker_image": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Docker image the service runs. Changing it upgrades the service in place.",
			},
			"topology": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "Deployment topology, `standalone` by default. Changing it replaces the service.",
			},
			"node_id": schema.Int64Attribute{
				Optional: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
				MarkdownDescription: "Node to run the service on; the control plane when unset. Changing it replaces the service.",
			},
			"parameters": schema.MapAttribute{
				Optional: true, Sensitive: true, ElementType: types.StringType,
				MarkdownDescription: "Engine parameters as strings, for example `database`, `username`, `password`, `max_connections`. " +
					"The provider converts each value to the type the server's parameter schema declares. " +
					"Sensitive parameters (such as `password`) are masked by the server and never cause drift; other " +
					"parameters you set are read back. Changing a parameter the server cannot change after creation " +
					"(`database`, `username`, `password`, `host`, or one its schema marks read-only) replaces the service. " +
					"Generated values (an auto-generated password) are not exposed here: " +
					"use `temps_service_connection`.",
			},
			"deletion_protection": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "Refuse to destroy the service while `true` (default).",
			},
			"status": schema.StringAttribute{Computed: true, MarkdownDescription: "Last observed status, for example `running`."},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// convertParameters turns the string map from configuration into the types
// the server's parameter schema declares.
func convertParameters(params map[string]string, schemaDef *client.ParameterSchema) (map[string]any, error) {
	out := make(map[string]any, len(params))
	for k, v := range params {
		prop, ok := schemaDef.Properties[k]
		if !ok {
			return nil, fmt.Errorf("parameter %q is not supported by this service type; supported: %v", k, keysOf(schemaDef.Properties))
		}
		types := prop.Types()
		switch {
		case slices.Contains(types, "string") || len(types) == 0:
			out[k] = v
		case slices.Contains(types, "integer"):
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parameter %q must be an integer, got %q", k, v)
			}
			out[k] = n
		case slices.Contains(types, "number"):
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, fmt.Errorf("parameter %q must be a number, got %q", k, v)
			}
			out[k] = f
		case slices.Contains(types, "boolean"):
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("parameter %q must be true or false, got %q", k, v)
			}
			out[k] = b
		default:
			var anyV any
			if err := json.Unmarshal([]byte(v), &anyV); err != nil {
				return nil, fmt.Errorf("parameter %q must be JSON (%v), got %q", k, types, v)
			}
			out[k] = anyV
		}
	}
	return out, nil
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func stringifyParam(v any) (string, bool) {
	switch t := v.(type) {
	case nil:
		return "", false
	case string:
		return t, true
	case bool:
		return strconv.FormatBool(t), true
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10), true
		}
		return strconv.FormatFloat(t, 'f', -1, 64), true
	default:
		b, err := json.Marshal(t)
		return string(b), err == nil
	}
}

// mergeParameters refreshes the configured parameters from the server:
// sensitive or masked values keep their configured value, others are read back.
func mergeParameters(configured map[string]string, d *client.ServiceDetails) map[string]string {
	if configured == nil {
		return nil
	}
	out := make(map[string]string, len(configured))
	for k, v := range configured {
		out[k] = v
		if slices.Contains(d.SensitiveParameters, k) {
			continue
		}
		if sv, ok := stringifyParam(d.CurrentParameters[k]); ok && sv != client.MaskedValue {
			out[k] = sv
		}
	}
	return out
}

// changedParameters returns the configured parameters that differ from state.
// Removed keys are ignored: the API cannot unset a parameter.
func changedParameters(planned, current map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range planned {
		if cur, ok := current[k]; !ok || cur != v {
			out[k] = v
		}
	}
	return out
}

// readOnlyParameters is the set of parameters the server refuses to change
// after creation. The schema's x-editable flag is authoritative, plus
// credentials and identity parameters the server also rejects even when the
// schema marks them editable (observed for the PostgreSQL password).
func readOnlyParameters(sch *client.ParameterSchema) map[string]bool {
	ro := map[string]bool{"database": true, "username": true, "password": true, "host": true}
	for k, p := range sch.Properties {
		if p.Editable != nil && !*p.Editable {
			ro[k] = true
		}
	}
	return ro
}

// ModifyPlan replaces the service when a parameter that cannot change after
// creation is changed, instead of letting the apply fail.
func (r *serviceResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	var plan, state serviceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !known(plan.Parameters) || plan.Parameters.Equal(state.Parameters) {
		return
	}
	planned := r.params(ctx, &plan, &resp.Diagnostics)
	current := r.params(ctx, &state, &resp.Diagnostics)
	changed := changedParameters(planned, current)
	if len(changed) == 0 {
		return
	}
	sch, err := r.client.GetServiceTypeParameters(ctx, state.ServiceType.ValueString())
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the parameter schema of service type "+state.ServiceType.ValueString(), err, servicePermissions)
		return
	}
	ro := readOnlyParameters(sch)
	for k := range changed {
		if ro[k] {
			resp.RequiresReplace = append(resp.RequiresReplace, path.Root("parameters"))
			return
		}
	}
}

func (r *serviceResource) params(ctx context.Context, m *serviceModel, diags *diag.Diagnostics) map[string]string {
	if !known(m.Parameters) {
		return nil
	}
	out := map[string]string{}
	diags.Append(m.Parameters.ElementsAs(ctx, &out, false)...)
	return out
}

func (m *serviceModel) fromAPI(d *client.ServiceDetails, configured map[string]string) {
	s := d.Service
	m.ID = types.StringValue(formatID(s.ID))
	m.Name = types.StringValue(s.Name)
	m.ServiceType = types.StringValue(s.ServiceType)
	m.Version = types.StringPointerValue(s.Version)
	m.Topology = types.StringValue(s.Topology)
	m.NodeID = types.Int64PointerValue(s.NodeID)
	m.Status = types.StringValue(s.Status)
	if img, ok := stringifyParam(d.CurrentParameters["docker_image"]); ok {
		m.DockerImage = types.StringValue(img)
	} else if m.DockerImage.IsUnknown() {
		m.DockerImage = types.StringNull()
	}
	if configured == nil {
		m.Parameters = types.MapNull(types.StringType)
		return
	}
	merged := mergeParameters(configured, d)
	vals := make(map[string]attr.Value, len(merged))
	for k, v := range merged {
		vals[k] = types.StringValue(v)
	}
	m.Parameters = types.MapValueMust(types.StringType, vals)
}

// waitRunning polls until the service is running; failed or stopped ends the wait with an error.
func (r *serviceResource) waitRunning(ctx context.Context, id int64, name string) (*client.ServiceDetails, error) {
	var last *client.ServiceDetails
	err := waitFor(ctx, fmt.Sprintf("temps_service %q (id %d) to become running", name, id), pollInterval, func() (bool, string, error) {
		d, err := r.client.GetService(ctx, id)
		if err != nil {
			return false, "", err
		}
		last = d
		switch d.Service.Status {
		case "running":
			return true, d.Service.Status, nil
		case "failed", "error", "stopped":
			msg := ""
			if d.Service.ErrorMessage != nil {
				msg = ": " + *d.Service.ErrorMessage
			}
			return false, d.Service.Status, fmt.Errorf("temps_service %q (id %d) is %s%s", name, id, d.Service.Status, msg)
		}
		return false, d.Service.Status, nil
	})
	return last, err
}

func (r *serviceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serviceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, defaultServiceTimeout)
	resp.Diagnostics.Append(d...)
	configured := r.params(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stype := plan.ServiceType.ValueString()
	sch, err := r.client.GetServiceTypeParameters(ctx, stype)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the parameter schema of service type "+stype, err, servicePermissions)
		return
	}
	params, err := convertParameters(configured, sch)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("parameters"), "Invalid service parameter", err.Error())
		return
	}
	if params == nil {
		params = map[string]any{}
	}
	if known(plan.DockerImage) {
		params["docker_image"] = plan.DockerImage.ValueString()
	}
	s, err := r.client.CreateService(ctx, client.CreateServiceRequest{
		Name: plan.Name.ValueString(), ServiceType: stype, Parameters: params,
		Topology: strPtr(plan.Topology), Version: strPtr(plan.Version), NodeID: int64Ptr(plan.NodeID),
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create Temps service "+plan.Name.String(), err, servicePermissions)
		return
	}
	// Track the service right away so a failed wait leaves it in state (tainted).
	plan.ID = types.StringValue(formatID(s.ID))
	details, err := r.waitRunning(ctx, s.ID, s.Name)
	if details != nil {
		plan.fromAPI(details, configured)
	} else {
		plan.fromAPI(&client.ServiceDetails{Service: *s}, configured)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Temps service "+plan.Name.String()+" did not become ready", err, servicePermissions)
	}
}

func (r *serviceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serviceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_service ID", err.Error())
		return
	}
	d, err := r.client.GetService(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read Temps service "+formatID(id), err, servicePermissions)
		return
	}
	state.fromAPI(d, r.params(ctx, &state, &resp.Diagnostics))
	if state.DeletionProtection.IsNull() {
		state.DeletionProtection = types.BoolValue(true) // import
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *serviceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state serviceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, defaultServiceTimeout)
	resp.Diagnostics.Append(d...)
	configured := r.params(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_service ID", err.Error())
		return
	}
	name := state.Name.ValueString()
	mutated := false

	changed := changedParameters(configured, r.params(ctx, &state, &resp.Diagnostics))
	if !plan.Parameters.Equal(state.Parameters) && len(changed) > 0 {
		sch, err := r.client.GetServiceTypeParameters(ctx, state.ServiceType.ValueString())
		if err != nil {
			addAPIError(&resp.Diagnostics, "Unable to read the parameter schema of service type "+state.ServiceType.ValueString(), err, servicePermissions)
			return
		}
		// Send only what changed: the server rejects read-only parameters
		// even when their value is unchanged.
		params, err := convertParameters(changed, sch)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("parameters"), "Invalid service parameter", err.Error())
			return
		}
		if _, err := r.client.UpdateServiceParameters(ctx, id, params); err != nil {
			addAPIError(&resp.Diagnostics, "Unable to update parameters of Temps service "+formatID(id), err, servicePermissions)
			return
		}
		mutated = true
	}
	if known(plan.DockerImage) && !plan.DockerImage.Equal(state.DockerImage) {
		if _, err := r.client.UpgradeService(ctx, id, plan.DockerImage.ValueString()); err != nil {
			addAPIError(&resp.Diagnostics, "Unable to upgrade Temps service "+formatID(id)+" to "+plan.DockerImage.String(), err, servicePermissions)
			return
		}
		mutated = true
	}

	var details *client.ServiceDetails
	if mutated {
		details, err = r.waitRunning(ctx, id, name)
	} else {
		details, err = r.client.GetService(ctx, id)
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Temps service "+formatID(id)+" did not become ready after update", err, servicePermissions)
		return
	}
	plan.fromAPI(details, configured)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serviceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Temps service is protected against deletion",
			fmt.Sprintf("temps_service %s (id %s) has deletion_protection = true. Destroying it deletes its data. "+
				"Set deletion_protection = false, run terraform apply, then destroy again.", state.Name, state.ID.ValueString()))
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, defaultServiceTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_service ID", err.Error())
		return
	}
	if err := r.client.DeleteService(ctx, id); err != nil {
		if client.IsNotFound(err) {
			return
		}
		addAPIError(&resp.Diagnostics, "Unable to delete Temps service "+formatID(id), err, servicePermissions)
		return
	}
	err = waitFor(ctx, fmt.Sprintf("temps_service %s (id %d) to be deleted", state.Name, id), pollInterval, func() (bool, string, error) {
		d, err := r.client.GetService(ctx, id)
		if client.IsNotFound(err) {
			return true, "deleted", nil
		}
		if err != nil {
			return false, "", err
		}
		return false, d.Service.Status, nil
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Temps service "+formatID(id)+" was not deleted", err, servicePermissions)
	}
}

func (r *serviceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := parseID(req.ID); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
