// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

// Settings of a managed service that live on their own endpoints:
// healthcheck overrides, server TLS, log ingestion budget and workload mode.

import (
	"context"
	"errors"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var serviceTLSModes = []string{"off", "available", "required"}

var serviceWorkloadModes = []string{"legacy", "shadow", "enforce"}

var healthCheckAttrTypes = map[string]attr.Type{
	"interval_secs": types.Int64Type, "start_interval_secs": types.Int64Type, "start_period_secs": types.Int64Type,
	"timeout_secs": types.Int64Type, "retries": types.Int64Type, "apply_immediately": types.BoolType,
}

var logIngestionAttrTypes = map[string]attr.Type{
	"lines_per_sec": types.Int64Type, "kib_per_sec": types.Int64Type,
}

type healthCheckModel struct {
	IntervalSecs      types.Int64 `tfsdk:"interval_secs"`
	StartIntervalSecs types.Int64 `tfsdk:"start_interval_secs"`
	StartPeriodSecs   types.Int64 `tfsdk:"start_period_secs"`
	TimeoutSecs       types.Int64 `tfsdk:"timeout_secs"`
	Retries           types.Int64 `tfsdk:"retries"`
	ApplyImmediately  types.Bool  `tfsdk:"apply_immediately"`
}

type logIngestionModel struct {
	LinesPerSec types.Int64 `tfsdk:"lines_per_sec"`
	KiBPerSec   types.Int64 `tfsdk:"kib_per_sec"`
}

// privateImported marks state created by an import, so the next read fills
// in settings that are otherwise only read once configured.
const privateImported = "imported"

func serviceSettingsAttributes() map[string]schema.Attribute {
	between := func(lo, hi int64) []validator.Int64 { return []validator.Int64{int64validator.Between(lo, hi)} }
	setting := []planmodifier.Int64{settingOf{}}
	return map[string]schema.Attribute{
		"health_check": schema.SingleNestedAttribute{
			Optional: true, Computed: true,
			PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
			MarkdownDescription: "The service's own Docker healthcheck overrides. Left out, Terraform does not manage them and keeps " +
				"whatever the service has. Once set, every value you leave out inherits the instance default (or the " +
				"engine's built-in value); `health_check = {}` clears them all. Docker cannot change a running " +
				"container's healthcheck, so a change applies when the container is next recreated, or right away with " +
				"`apply_immediately`. Only standalone services whose engine has a Docker healthcheck (not S3-compatible " +
				"stores, clusters or services on worker nodes) take overrides; Temps refuses them otherwise. " +
				"Read back from `GET /external-services/{id}/healthcheck`.",
			Attributes: map[string]schema.Attribute{
				"interval_secs": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: setting, Validators: between(5, 3600),
					MarkdownDescription: "Seconds between checks once the container is healthy (5–3600)."},
				"start_interval_secs": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: setting, Validators: between(1, 60),
					MarkdownDescription: "Seconds between checks while the container starts (1–60)."},
				"start_period_secs": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: setting, Validators: between(0, 600),
					MarkdownDescription: "Seconds after start during which failed checks do not count (0–600)."},
				"timeout_secs": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: setting, Validators: between(1, 60),
					MarkdownDescription: "Seconds one check may run (1–60); it must stay shorter than the interval."},
				"retries": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: setting, Validators: between(1, 10),
					MarkdownDescription: "Consecutive failed checks that make the container unhealthy (1–10)."},
				"apply_immediately": schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Bool{settingOf{}},
					MarkdownDescription: "When `true`, recreate the container right after the overrides change so they take " +
						"effect now. **The service restarts briefly** (its volumes and configuration are kept). Temps " +
						"only does this for a running standalone service on the control plane. Not sent to Temps; while " +
						"Temps reports overrides waiting for a recreate (an earlier recreate failed, or they were saved " +
						"elsewhere) and the service can be recreated now, it reads back as `false`, so the next apply recreates it."},
			},
		},
		"tls_mode": schema.StringAttribute{
			Optional: true, Computed: true,
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			Validators:    []validator.String{stringvalidator.OneOf(serviceTLSModes...)},
			MarkdownDescription: "Managed server TLS: `off`, `available` (TLS offered, plaintext still accepted) or `required` " +
				"(which current Temps servers refuse). Left out, Terraform keeps the service's current mode. " +
				"**Changing it restarts a standalone database**; a cluster's data nodes switch certificates in place. " +
				"Read back from `GET /external-services/{id}/tls`.",
		},
		"log_ingestion": schema.SingleNestedAttribute{
			Optional: true, Computed: true,
			PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
			MarkdownDescription: "The service's own container log ingestion budget. Left out, Terraform keeps the service's " +
				"current budget. Once set, a value you leave out uses the platform budget (Settings > Monitoring); " +
				"`log_ingestion = {}` returns both to it. Running containers pick a change up within a minute; " +
				"nothing restarts. Read back from `GET /external-services/{id}/log-ingestion`.",
			Attributes: map[string]schema.Attribute{
				"lines_per_sec": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: setting, Validators: between(0, 1000000),
					MarkdownDescription: "Log lines per second per container (0–1,000,000); `0` lifts the limit."},
				"kib_per_sec": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: setting, Validators: between(0, 1048576),
					MarkdownDescription: "KiB of log text per second per container (0–1,048,576); `0` lifts the limit."},
			},
		},
		"workload_mode": schema.StringAttribute{
			Optional:   true,
			Validators: []validator.String{stringvalidator.OneOf(serviceWorkloadModes...)},
			MarkdownDescription: "Operator-only migration control for how the database's container is built: `legacy` " +
				"(the default), `shadow` or `enforce`. Reading and changing it needs an API key of an instance " +
				"administrator, so it is only read back (from `GET /external-services/{id}/workload-mode`) while it is " +
				"set. Changing it does not restart anything: the container follows the mode when it is next recreated " +
				"from its workload spec, a step an operator confirms with the Temps CLI. Removing the argument stops managing the mode and " +
				"leaves the database's current mode in place.",
		},
	}
}

// --- conversions ---

func (h *healthCheckModel) overrides() client.HealthcheckOverrides {
	return client.HealthcheckOverrides{
		IntervalSecs: int64Ptr(h.IntervalSecs), StartIntervalSecs: int64Ptr(h.StartIntervalSecs),
		StartPeriodSecs: int64Ptr(h.StartPeriodSecs), TimeoutSecs: int64Ptr(h.TimeoutSecs), Retries: int64Ptr(h.Retries),
	}
}

func healthCheckValue(o client.HealthcheckOverrides, applyImmediately types.Bool) types.Object {
	return types.ObjectValueMust(healthCheckAttrTypes, map[string]attr.Value{
		"interval_secs": types.Int64PointerValue(o.IntervalSecs), "start_interval_secs": types.Int64PointerValue(o.StartIntervalSecs),
		"start_period_secs": types.Int64PointerValue(o.StartPeriodSecs), "timeout_secs": types.Int64PointerValue(o.TimeoutSecs),
		"retries": types.Int64PointerValue(o.Retries), "apply_immediately": applyImmediately,
	})
}

func (l *logIngestionModel) budget() client.ServiceLogIngestion {
	return client.ServiceLogIngestion{LinesPerSec: int64Ptr(l.LinesPerSec), KiBPerSec: int64Ptr(l.KiBPerSec)}
}

func logIngestionValue(l client.ServiceLogIngestion) types.Object {
	return types.ObjectValueMust(logIngestionAttrTypes, map[string]attr.Value{
		"lines_per_sec": types.Int64PointerValue(l.LinesPerSec), "kib_per_sec": types.Int64PointerValue(l.KiBPerSec),
	})
}

func objectAs[T any](ctx context.Context, o types.Object, diags *diag.Diagnostics) (*T, bool) {
	if !known(o) {
		return nil, false
	}
	var out T
	diags.Append(o.As(ctx, &out, basetypes.ObjectAsOptions{})...)
	return &out, !diags.HasError()
}

func samePtr(a, b *int64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func sameOverrides(a, b client.HealthcheckOverrides) bool {
	return samePtr(a.IntervalSecs, b.IntervalSecs) && samePtr(a.StartIntervalSecs, b.StartIntervalSecs) &&
		samePtr(a.StartPeriodSecs, b.StartPeriodSecs) && samePtr(a.TimeoutSecs, b.TimeoutSecs) && samePtr(a.Retries, b.Retries)
}

// --- writes ---

// applySettings sends the settings that are configured and differ from
// prior (nil on create, when the service has its defaults). It reports
// whether the service was restarted.
func (r *serviceResource) applySettings(ctx context.Context, id int64, plan, prior *serviceModel, diags *diag.Diagnostics) bool {
	restarted := false
	what := "Temps service " + formatID(id)

	if hc, ok := objectAs[healthCheckModel](ctx, plan.HealthCheck, diags); ok {
		want := hc.overrides()
		var have client.HealthcheckOverrides
		if prior != nil {
			if p, ok := objectAs[healthCheckModel](ctx, prior.HealthCheck, diags); ok {
				have = p.overrides()
			} else if cur, err := r.client.GetServiceHealthcheck(ctx, id); err == nil {
				have = cur.Overrides
			}
		}
		changed := !sameOverrides(want, have)
		if changed {
			if _, err := r.client.SetServiceHealthcheck(ctx, id, want); err != nil {
				addAPIError(diags, "Unable to set the healthcheck of "+what, err, servicePermissions)
				return restarted
			}
		}
		// Unchanged overrides still need a recreate when an earlier one
		// failed or they were saved without applying (readSettings then
		// records apply_immediately = false, so this update was planned).
		if !changed && hc.ApplyImmediately.ValueBool() && prior != nil {
			cur, err := r.client.GetServiceHealthcheck(ctx, id)
			if err != nil {
				addAPIError(diags, "Unable to read the healthcheck of "+what, err, servicePermissions)
				return restarted
			}
			changed = cur.PendingRecreate
		}
		if changed {
			if hc.ApplyImmediately.ValueBool() {
				if err := r.client.ApplyServiceHealthcheck(ctx, id); err != nil {
					addAPIError(diags, "Saved the healthcheck of "+what+" but could not recreate its container to apply it now", err, servicePermissions)
					return restarted
				}
				restarted = true
			}
		}
	}

	if known(plan.TLSMode) {
		have := ""
		if prior != nil && known(prior.TLSMode) {
			have = prior.TLSMode.ValueString()
		} else {
			cur, err := r.client.GetServiceTLS(ctx, id)
			if err != nil {
				addAPIError(diags, "Unable to read the TLS mode of "+what, err, servicePermissions)
				return restarted
			}
			have = cur.Mode
		}
		if want := plan.TLSMode.ValueString(); want != have {
			if _, err := r.client.SetServiceTLS(ctx, id, want); err != nil {
				addAPIError(diags, "Unable to change the TLS mode of "+what+" to "+want, err, servicePermissions)
				return restarted
			}
			restarted = true
		}
	}

	if li, ok := objectAs[logIngestionModel](ctx, plan.LogIngestion, diags); ok {
		want := li.budget()
		var have client.ServiceLogIngestion
		if prior != nil {
			if p, ok := objectAs[logIngestionModel](ctx, prior.LogIngestion, diags); ok {
				have = p.budget()
			} else if cur, err := r.client.GetServiceLogIngestion(ctx, id); err == nil {
				have = *cur
			}
		}
		if !samePtr(want.LinesPerSec, have.LinesPerSec) || !samePtr(want.KiBPerSec, have.KiBPerSec) {
			if _, err := r.client.SetServiceLogIngestion(ctx, id, want); err != nil {
				addAPIError(diags, "Unable to set the log ingestion budget of "+what, err, servicePermissions)
				return restarted
			}
		}
	}

	if known(plan.WorkloadMode) {
		want := plan.WorkloadMode.ValueString()
		// A new database starts in legacy mode.
		have := "legacy"
		if prior != nil {
			have = prior.WorkloadMode.ValueString()
		}
		if want != have {
			if _, err := r.client.SetServiceWorkloadMode(ctx, id, want); err != nil {
				addAPIError(diags, "Unable to change the workload mode of "+what+" to "+want+
					" (Temps requires an API key of an instance administrator)", err, servicePermissions)
				return restarted
			}
		}
	}
	return restarted
}

// --- reads ---

// readSettings refreshes the settings from Temps. A server without one of
// the endpoints leaves that setting as it was (null when never known).
// imported fills in a set workload mode, which is otherwise only read while
// configured.
func (r *serviceResource) readSettings(ctx context.Context, id int64, m *serviceModel, imported bool, diags *diag.Diagnostics) {
	what := "Temps service " + formatID(id)
	keep := func(v types.Object, attrTypes map[string]attr.Type) types.Object {
		if v.IsUnknown() {
			return types.ObjectNull(attrTypes)
		}
		return v
	}

	apply := types.BoolNull()
	if hc, ok := objectAs[healthCheckModel](ctx, m.HealthCheck, diags); ok {
		apply = hc.ApplyImmediately
	}
	switch hc, err := r.client.GetServiceHealthcheck(ctx, id); {
	case err == nil:
		if apply.ValueBool() && hc.PendingRecreate && hc.Capability.CanApplyNow {
			// The overrides are saved but the container still runs the old
			// healthcheck: plan the recreate apply_immediately asks for.
			apply = types.BoolValue(false)
		}
		m.HealthCheck = healthCheckValue(hc.Overrides, apply)
	case client.IsUnsupported(err):
		m.HealthCheck = keep(m.HealthCheck, healthCheckAttrTypes)
	default:
		addAPIError(diags, "Unable to read the healthcheck of "+what, err, servicePermissions)
		return
	}

	switch t, err := r.client.GetServiceTLS(ctx, id); {
	case err == nil:
		m.TLSMode = types.StringValue(t.Mode)
	case client.IsUnsupported(err):
		if m.TLSMode.IsUnknown() {
			m.TLSMode = types.StringNull()
		}
	default:
		addAPIError(diags, "Unable to read the TLS mode of "+what, err, servicePermissions)
		return
	}

	switch l, err := r.client.GetServiceLogIngestion(ctx, id); {
	case err == nil:
		m.LogIngestion = logIngestionValue(*l)
	case client.IsUnsupported(err):
		m.LogIngestion = keep(m.LogIngestion, logIngestionAttrTypes)
	default:
		addAPIError(diags, "Unable to read the log ingestion budget of "+what, err, servicePermissions)
		return
	}

	switch {
	case known(m.WorkloadMode):
		wm, err := r.client.GetServiceWorkloadMode(ctx, id)
		if err != nil && !client.IsUnsupported(err) {
			addAPIError(diags, "Unable to read the workload mode of "+what+
				" (Temps requires an API key of an instance administrator)", err, servicePermissions)
			return
		}
		if err == nil {
			m.WorkloadMode = types.StringValue(wm.Mode)
		}
	case imported:
		// Best effort: only an administrator can read it, and the default
		// is left unset so a typical import plans no change.
		m.WorkloadMode = types.StringNull()
		if wm, err := r.client.GetServiceWorkloadMode(ctx, id); err == nil && wm.Mode != "legacy" {
			m.WorkloadMode = types.StringValue(wm.Mode)
		} else if err != nil && !isForbiddenOrUnsupported(err) {
			addAPIError(diags, "Unable to read the workload mode of "+what, err, servicePermissions)
		}
	default:
		m.WorkloadMode = types.StringNull()
	}
}

func isForbiddenOrUnsupported(err error) bool {
	var apiErr *client.APIError
	return client.IsUnsupported(err) || (errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden)
}

// nullSettings replaces unknown settings with null, for a state saved after
// a failed create.
func (m *serviceModel) nullSettings() {
	if m.HealthCheck.IsUnknown() {
		m.HealthCheck = types.ObjectNull(healthCheckAttrTypes)
	}
	if m.TLSMode.IsUnknown() {
		m.TLSMode = types.StringNull()
	}
	if m.LogIngestion.IsUnknown() {
		m.LogIngestion = types.ObjectNull(logIngestionAttrTypes)
	}
	if m.WorkloadMode.IsUnknown() {
		m.WorkloadMode = types.StringNull()
	}
}
