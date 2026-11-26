// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

// Metric alert rules (nodes, managed services, OpenTelemetry) and
// OpenTelemetry dashboards.

// metricRuleFields are the fields node and managed service alert rules share.
func metricRuleFields(metricDesc, thresholdDesc string) []gfield {
	return []gfield{
		{Name: "name", Kind: kString, Required: true, Desc: "Name shown in notifications."},
		{Name: "metric_name", Kind: kString, Required: true, Desc: metricDesc},
		{Name: "threshold", Kind: kFloat, Required: true, Desc: thresholdDesc},
		{Name: "comparator", Kind: kString, Required: true, OneOf: []string{">", "<", ">=", "<="},
			Desc: "How the metric is compared with `threshold`."},
		{Name: "severity", Kind: kString, Required: true, OneOf: []string{"warning", "critical"}, Desc: "Severity of the alarm."},
		{Name: "for_duration_secs", Kind: kInt, Optional: true, Default: int64(0),
			Desc: "Seconds the breach must last before the alarm fires; `0` fires at once."},
		{Name: "enabled", Kind: kBool, Optional: true, Default: true, Desc: "Evaluate the rule."},
		{Name: "silenced_until", Kind: kString, Computed: true, Volatile: true, Desc: "Time the rule is silenced until, if it is."},
	}
}

// diskHealthNodeMetrics have default rules Temps creates again when a node has
// none of them left, so it refuses to delete them.
var diskHealthNodeMetrics = []string{"node.raid_degraded", "node.disk_wear_percent", "node.disk_media_errors"}

const nodeAlertRulePath = "/nodes/{node_id}/metrics/alert-rules/{id}"

var nodeAlertRuleSpec = &gspec{
	Name: "node_alert_rule",
	Desc: "An alert on a node's resource usage or disk health. A node has at most one rule per metric: to " +
		"manage a rule Temps already created (the control plane's defaults, a worker's disk-health rules), " +
		"import it.",
	DeleteNote: "Temps refuses to delete the control plane's rules and a worker's disk-health rules, because it " +
		"creates them again; destroying one of those disables it instead. Other rules are deleted.",
	Perms:   []string{"settings:read", "settings:write"},
	Parents: []string{"node_id"},
	Fields: append([]gfield{
		{Name: "node_id", Kind: kInt, Required: true, Path: true, ZeroOK: true,
			Desc: "ID of the node; `0` is the control plane."},
	}, metricRuleFields(
		"Metric watched: `node.cpu_percent`, `node.memory_percent`, `node.disk_percent`, `node.raid_degraded`, "+
			"`node.disk_wear_percent` or `node.disk_media_errors`.",
		"Threshold: 0-100 for a usage percentage, 0 or more for a disk-health metric.")...),
	Create: gop{Method: http.MethodPost, Path: "/nodes/{node_id}/metrics/alert-rules"},
	Read:   gop{Path: nodeAlertRulePath},
	Update: gop{Method: http.MethodPatch, Path: nodeAlertRulePath},
	DeleteHook: func(ctx context.Context, c *client.Client, v gvals, id string) error {
		p := renderPath(nodeAlertRulePath, v, id)
		if scalarString(v["node_id"]) == "0" || slices.Contains(diskHealthNodeMetrics, scalarString(v["metric_name"])) {
			_, err := c.Raw(ctx, http.MethodPatch, p, nil, map[string]any{"enabled": false})
			return err
		}
		_, err := c.Raw(ctx, http.MethodDelete, p, nil, nil)
		return err
	},
}

const serviceAlertRulesPath = "/external-services/{service_id}/metrics/alert-rules"

var serviceAlertRuleSpec = &gspec{
	Name: "service_alert_rule",
	Desc: "An alert on a metric of a managed service (`temps_service`), such as its connection count or " +
		"replication lag. The service needs metric collection turned on for the rule to be evaluated.",
	Perms:   []string{"external_services:read", "external_services:write"},
	Parents: []string{"service_id"},
	Fields: append([]gfield{
		{Name: "service_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the managed service."},
	}, metricRuleFields("Metric watched, as collected for the service's engine.", "Threshold the metric is compared with.")...),
	Create: gop{Method: http.MethodPost, Path: serviceAlertRulesPath},
	Read:   gop{Path: serviceAlertRulesPath},
	List:   true,
	Update: gop{Method: http.MethodPut, Path: serviceAlertRulesPath + "/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: serviceAlertRulesPath + "/{id}"},
}

// projectQuery scopes a by-ID request to the owning project, as the
// OpenTelemetry endpoints require.
func projectQuery(v gvals) url.Values {
	return url.Values{"project_id": {scalarString(v["project_id"])}}
}

var otelPerms = []string{"otel:read", "otel:write"}

// What Temps uses for an alert rule's series settings left out of a create
// (its update leaves them unchanged, so they are Terraform defaults too), and
// the series it gives a log alert (temps-otel's metric alert service).
const (
	defaultMaxSeries        = 20
	defaultGroupedThreshold = 5
	logAlertMetricName      = "logs.matched_lines"
	logAlertAggregation     = "count"
)

// validateOtelAlertRule refuses what Temps would silently rewrite: a log
// alert's series is named by Temps and counts lines, so a configured
// metric_name or another aggregation would come back different from the plan.
func validateOtelAlertRule(v gvals, diags *diag.Diagnostics) {
	src, ok := v["source"].(types.String)
	if !ok || !known(src) {
		return
	}
	var doc map[string]any
	if json.Unmarshal([]byte(src.ValueString()), &doc) != nil || doc["kind"] != "logs" {
		return
	}
	if m, ok := v["metric_name"].(types.String); ok && known(m) {
		diags.AddAttributeError(path.Root("metric_name"), "metric_name is not used by a log alert",
			"A rule whose source is a saved log search watches the matching line count, which Temps names `"+
				logAlertMetricName+"`. Remove metric_name.")
	}
	if a, ok := v["aggregation"].(types.String); ok && known(a) && a.ValueString() != logAlertAggregation {
		diags.AddAttributeError(path.Root("aggregation"), "A log alert counts lines",
			fmt.Sprintf("A rule whose source is a saved log search must use aggregation = %q, not %q.",
				logAlertAggregation, a.ValueString()))
	}
}

var otelAlertRuleSpec = &gspec{
	Name: "otel_alert_rule",
	Desc: "An alert on an OpenTelemetry metric a project sends to Temps, or on the number of log lines " +
		"matching a saved search. Today only the `static` detector is evaluated.",
	Perms:   otelPerms,
	Parents: []string{"project_id"},
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, CreateOnly: true, Desc: "ID of the project."},
		{Name: "name", Kind: kString, Required: true, Desc: "Name of the rule."},
		{Name: "metric_name", Kind: kString, Optional: true, Volatile: true,
			Desc: "OpenTelemetry metric watched. Required unless `source` is a saved log search: leave it unset " +
				"there, Temps names that series `" + logAlertMetricName + "`."},
		{Name: "aggregation", Kind: kString, Required: true,
			OneOf: []string{"avg", "sum", "min", "max", "count", "rate", "p50", "p90", "p95", "p99"},
			Desc:  "How the metric's points in a window are combined. A log alert counts lines: use `count`."},
		{Name: "source", Kind: kJSON, Optional: true, Default: `{"kind":"metric"}`, Subset: true,
			Desc: "What the rule watches: `jsonencode({ kind = \"metric\" })` (the default) or " +
				"`jsonencode({ kind = \"logs\", saved_search_id = 3 })`."},
		{Name: "detection_config", Kind: kJSON, Required: true, Subset: true,
			Desc: "The detector, for example `jsonencode({ kind = \"static\", comparator = \"gt\", threshold = 500 })`. " +
				"Comparators: `gt`, `gte`, `lt`, `lte`."},
		{Name: "window_secs", Kind: kInt, Required: true, Desc: "Length of the window the metric is aggregated over, in seconds."},
		{Name: "for_duration_secs", Kind: kInt, Optional: true, Default: int64(0),
			Desc: "Seconds the breach must last before the alarm fires; `0` fires at once."},
		{Name: "severity", Kind: kString, Required: true, OneOf: []string{"info", "warning", "critical"}, Desc: "Severity of the alarm."},
		{Name: "enabled", Kind: kBool, Optional: true, Default: true, Desc: "Evaluate the rule."},
		{Name: "label_filters", Kind: kJSON, Optional: true, Default: "[]",
			Desc: "Label equality filters, all of which must match: `jsonencode([[\"region\", \"eu\"]])`. At most 10. " +
				"The default, `[]`, matches every series."},
		{Name: "group_by", Kind: kStringList, Optional: true, Default: []string{},
			Desc: "Label keys to break the metric down by, at most 2. The default, `[]`, watches the aggregate."},
		{Name: "dynamic_alerts", Kind: kBool, Optional: true, Default: false,
			Desc: "With `group_by`, fire one alarm per breaching series instead of one for the aggregate. Defaults to `false`."},
		{Name: "max_series", Kind: kInt, Optional: true, Default: int64(defaultMaxSeries),
			Desc: fmt.Sprintf("Most series evaluated with `dynamic_alerts`, 1-100. Defaults to `%d`.", defaultMaxSeries)},
		{Name: "grouped_notification_threshold", Kind: kInt, Optional: true, Default: int64(defaultGroupedThreshold),
			Desc: fmt.Sprintf("When more series than this start firing at once, only the first notification gets charts, "+
				"1-1000. Defaults to `%d`.", defaultGroupedThreshold)},
		{Name: "detection_kind", Kind: kString, Computed: true, Desc: "Kind of the detector."},
		{Name: "last_state", Kind: kString, Computed: true, Volatile: true, Desc: "`ok`, `firing` or `unknown`."},
	},
	Validate: validateOtelAlertRule,
	Create:   gop{Method: http.MethodPost, Path: "/otel/alerts"},
	Read:     gop{Path: "/otel/alerts/{id}", Query: projectQuery},
	Update:   gop{Method: http.MethodPatch, Path: "/otel/alerts/{id}", Query: projectQuery},
	Delete:   gop{Method: http.MethodDelete, Path: "/otel/alerts/{id}", Query: projectQuery},
}

// The keys Temps keeps in a dashboard layout (temps-otel's DashboardLayout,
// DashboardSection and DashboardTile). It drops any other key without an
// error, so the layout read back would differ from the configured one.
var (
	dashboardLayoutKeys  = []string{"sections"}
	dashboardSectionKeys = []string{"id", "title", "tiles"}
	dashboardTileKeys    = []string{"id", "metric_name", "aggregation", "title", "label_filters", "group_by", "source"}
)

// validateOtelDashboard refuses layout keys Temps would drop.
func validateOtelDashboard(v gvals, diags *diag.Diagnostics) {
	l, ok := v["layout"].(types.String)
	if !ok || !known(l) {
		return
	}
	var layout map[string]any
	if json.Unmarshal([]byte(l.ValueString()), &layout) != nil {
		return // the attribute's own validator reports it
	}
	unknownKeys := func(where string, obj map[string]any, allowed []string) {
		for _, k := range sortedKeys(obj) {
			if !slices.Contains(allowed, k) {
				diags.AddAttributeError(path.Root("layout"), "Unknown dashboard layout key",
					fmt.Sprintf("%s has %q, which Temps does not store; it accepts %s.", where, k, "`"+joinTicks(allowed)+"`"))
			}
		}
	}
	unknownKeys("The layout", layout, dashboardLayoutKeys)
	sections, _ := layout["sections"].([]any)
	for i, s := range sections {
		section, _ := s.(map[string]any)
		unknownKeys(fmt.Sprintf("Section %d", i+1), section, dashboardSectionKeys)
		tiles, _ := section["tiles"].([]any)
		for j, t := range tiles {
			tile, _ := t.(map[string]any)
			unknownKeys(fmt.Sprintf("Tile %d of section %d", j+1, i+1), tile, dashboardTileKeys)
		}
	}
}

var otelDashboardSpec = &gspec{
	Name:    "otel_dashboard",
	Desc:    "A saved dashboard of a project's OpenTelemetry metrics and logs: sections of chart tiles.",
	Perms:   otelPerms,
	Parents: []string{"project_id"},
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, CreateOnly: true, Desc: "ID of the project."},
		{Name: "name", Kind: kString, Required: true, Desc: "Name of the dashboard."},
		{Name: "layout", Kind: kJSON, Required: true, Subset: true,
			Desc: "Sections and their tiles: `jsonencode({ sections = [{ id = \"http\", title = \"HTTP\", tiles = [...] }] })`. " +
				"A tile has `id`, `metric_name`, `aggregation` and optionally `title`, `label_filters`, `group_by` and " +
				"`source`. Values Temps fills in for a tile left out of the configuration are not compared."},
	},
	Validate: validateOtelDashboard,
	Create:   gop{Method: http.MethodPost, Path: "/otel/dashboards"},
	Read:     gop{Path: "/otel/dashboards/{id}", Query: projectQuery},
	Update:   gop{Method: http.MethodPatch, Path: "/otel/dashboards/{id}", Query: projectQuery},
	Delete:   gop{Method: http.MethodDelete, Path: "/otel/dashboards/{id}", Query: projectQuery},
}
