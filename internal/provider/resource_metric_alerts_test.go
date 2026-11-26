// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestUnitNodeAlertRule(t *testing.T) {
	f := newMoreFake(t)
	cfg := func(threshold string) string {
		return f.config(`
resource "temps_node_alert_rule" "cpu" {
  node_id     = 7
  name        = "CPU high"
  metric_name = "node.cpu_percent"
  threshold   = %s
  comparator  = ">"
  severity    = "warning"
}`, threshold)
	}
	const addr = "temps_node_alert_rule.cpu"
	var id string
	runEngine(t, []resource.TestStep{
		{
			Config:           cfg("90"),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(addr, "for_duration_secs", "0"),
				resource.TestCheckResourceAttr(addr, "enabled", "true"),
				func(s *terraform.State) error { id = idOf(s, addr); return nil },
			),
		},
		{
			Config:           cfg("82.5"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
			Check:            resource.TestCheckResourceAttr(addr, "threshold", "82.5"),
		},
		{ResourceName: addr, ImportState: true, ImportStateVerify: true},
		{
			PreConfig:        func() { f.with(func() { f.nodeRules[mustID(id[2:])]["enabled"] = false }) },
			Config:           cfg("82.5"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
		},
		{
			// A second rule for the same metric is refused.
			Config: cfg("82.5") + `
resource "temps_node_alert_rule" "dup" {
  node_id     = 7
  name        = "CPU again"
  metric_name = "node.cpu_percent"
  threshold   = 50
  comparator  = ">"
  severity    = "critical"
}`,
			ExpectError: regexp.MustCompile(`(?s)409.*already has an alert rule`),
		},
	}, func(*terraform.State) error {
		var n int
		f.with(func() { n = len(f.nodeRules) })
		if n != 0 {
			return fmt.Errorf("%d node rules left after destroy", n)
		}
		if f.count("DELETE /api/nodes/7/metrics/alert-rules/"+id[2:]) != 1 {
			return fmt.Errorf("the worker's rule was not deleted")
		}
		return nil
	})
}

// TestUnitNodeAlertRuleProtected checks that the rules Temps refuses to
// delete (the control plane's, a worker's disk-health rules) are imported,
// changed and, on destroy, disabled.
func TestUnitNodeAlertRuleProtected(t *testing.T) {
	f := newMoreFake(t)
	var cp, disk int64
	f.with(func() {
		cp, disk = f.next(), f.next()
		f.nodeRules[cp] = map[string]any{"id": cp, "node_id": 0, "name": "Proxy errors", "metric_name": "proxy.error_rate",
			"threshold": 5.0, "comparator": ">", "severity": "critical", "for_duration_secs": 60, "enabled": true, "silenced_until": nil}
		f.nodeRules[disk] = map[string]any{"id": disk, "node_id": 7, "name": "RAID degraded", "metric_name": "node.raid_degraded",
			"threshold": 0.0, "comparator": ">", "severity": "critical", "for_duration_secs": 0, "enabled": true, "silenced_until": nil}
	})
	cfg := f.config(`
import {
  to = temps_node_alert_rule.proxy
  id = "0/%d"
}

resource "temps_node_alert_rule" "proxy" {
  node_id           = 0
  name              = "Proxy errors"
  metric_name       = "proxy.error_rate"
  threshold         = 2.5
  comparator        = ">"
  severity          = "critical"
  for_duration_secs = 60
}

import {
  to = temps_node_alert_rule.raid
  id = "7/%d"
}

resource "temps_node_alert_rule" "raid" {
  node_id     = 7
  name        = "RAID degraded"
  metric_name = "node.raid_degraded"
  threshold   = 0
  comparator  = ">"
  severity    = "warning"
}`, cp, disk)
	runEngine(t, []resource.TestStep{
		{
			Config:           cfg,
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: func(*terraform.State) error {
				var th, sev any
				f.with(func() { th, sev = f.nodeRules[cp]["threshold"], f.nodeRules[disk]["severity"] })
				if fmt.Sprint(th) != "2.5" || sev != "warning" {
					return fmt.Errorf("imported rules were not updated: %v %v", th, sev)
				}
				return nil
			},
		},
	}, func(*terraform.State) error {
		var rules map[int64]map[string]any
		f.with(func() { rules = f.nodeRules })
		for _, id := range []int64{cp, disk} {
			r, ok := rules[id]
			if !ok {
				return fmt.Errorf("protected rule %d was deleted", id)
			}
			if r["enabled"] != false {
				return fmt.Errorf("protected rule %d was not disabled on destroy", id)
			}
		}
		if n := f.count(fmt.Sprintf("DELETE /api/nodes/0/metrics/alert-rules/%d", cp)); n != 0 {
			return fmt.Errorf("destroy sent DELETE for the control plane's rule")
		}
		return nil
	})
}

func TestUnitServiceAlertRule(t *testing.T) {
	f := newMoreFake(t)
	cfg := func(threshold, extra string) string {
		return f.config(`
resource "temps_service_alert_rule" "conn" {
  service_id  = 21
  name        = "Connections"
  metric_name = "pg.connections"
  threshold   = %s
  comparator  = ">="
  severity    = "critical"
  %s
}`, threshold, extra)
	}
	const addr = "temps_service_alert_rule.conn"
	var id int64
	runEngine(t, []resource.TestStep{
		{
			Config:           cfg("80", ""),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: func(s *terraform.State) error {
				id = mustID(idOf(s, addr)[3:])
				return nil
			},
		},
		{
			Config:           cfg("95", "for_duration_secs = 300\n  enabled = false"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
			Check: func(*terraform.State) error {
				b := f.body(fmt.Sprintf("PUT /api/external-services/21/metrics/alert-rules/%d", id))
				if fmt.Sprint(b["threshold"]) != "95" || fmt.Sprint(b["for_duration_secs"]) != "300" || b["enabled"] != false {
					return fmt.Errorf("update sent %v", b)
				}
				return nil
			},
		},
		{ResourceName: addr, ImportState: true, ImportStateVerify: true},
		{
			PreConfig:        func() { f.with(func() { f.svcRules[id]["severity"] = "warning" }) },
			Config:           cfg("95", "for_duration_secs = 300\n  enabled = false"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
		},
		{
			PreConfig:        func() { f.with(func() { delete(f.svcRules, id) }) },
			Config:           cfg("95", ""),
			ConfigPlanChecks: plannedCreate(addr),
		},
		{Config: f.config(`
resource "temps_service_alert_rule" "conn" {
  service_id  = 99
  name        = "Connections"
  metric_name = "pg.connections"
  threshold   = 1
  comparator  = ">"
  severity    = "critical"
}`), ExpectError: regexp.MustCompile(`(?s)404.*external service`)},
	}, func(*terraform.State) error {
		var n int
		f.with(func() { n = len(f.svcRules) })
		if n != 0 {
			return fmt.Errorf("%d service rules left after destroy", n)
		}
		return nil
	})
}

func TestUnitOtelAlertRule(t *testing.T) {
	f := newMoreFake(t)
	cfg := func(project int, severity string) string {
		return f.config(`
resource "temps_otel_alert_rule" "latency" {
  project_id       = %d
  name             = "Slow checkout"
  metric_name      = "http.server.duration"
  aggregation      = "p95"
  detection_config = jsonencode({ kind = "static", comparator = "gt", threshold = 500 })
  window_secs      = 300
  severity         = %q
  label_filters    = jsonencode([["route", "/checkout"]])
  group_by         = ["region"]
}`, project, severity)
	}
	const addr = "temps_otel_alert_rule.latency"
	var id int64
	runEngine(t, []resource.TestStep{
		{
			Config:           cfg(8, "warning"),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(addr, "detection_kind", "static"),
				resource.TestCheckResourceAttr(addr, "max_series", "20"),
				resource.TestCheckResourceAttr(addr, "dynamic_alerts", "false"),
				resource.TestCheckResourceAttr(addr, "source", `{"kind":"metric"}`),
				func(s *terraform.State) error {
					id = mustID(idOf(s, addr)[2:])
					if b := f.body("POST /api/otel/alerts"); fmt.Sprint(b["project_id"]) != "8" {
						return fmt.Errorf("create sent project_id %v", b["project_id"])
					}
					return nil
				},
			),
		},
		{
			Config:           cfg(8, "critical"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
			Check: func(*terraform.State) error {
				b := f.body(fmt.Sprintf("PATCH /api/otel/alerts/%d?project_id=8", id))
				if b["severity"] != "critical" {
					return fmt.Errorf("update sent %v", b)
				}
				if f.count(fmt.Sprintf("GET /api/otel/alerts/%d?project_id=8", id)) == 0 {
					return fmt.Errorf("reads must name the project")
				}
				return nil
			},
		},
		{ResourceName: addr, ImportState: true, ImportStateVerify: true},
		{
			PreConfig: func() {
				f.with(func() {
					f.otelRules[id]["detection_config"] = map[string]any{"kind": "static", "comparator": "gt", "threshold": 900}
				})
			},
			Config:           cfg(8, "critical"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
		},
		{
			Config:           cfg(9, "critical"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionDestroyBeforeCreate),
		},
		{
			PreConfig:        func() { f.with(func() { clear(f.otelRules) }) },
			Config:           cfg(9, "critical"),
			ConfigPlanChecks: plannedCreate(addr),
		},
		{Config: cfg(9, "critical")},
		{
			// A log alert: Temps names the series and counts lines.
			Config: cfg(9, "critical") + `
resource "temps_otel_alert_rule" "errors" {
  project_id       = 9
  name             = "Error lines"
  aggregation      = "count"
  source           = jsonencode({ kind = "logs", saved_search_id = 3 })
  detection_config = jsonencode({ kind = "static", comparator = "gte", threshold = 10 })
  window_secs      = 60
  severity         = "warning"
}`,
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check:            resource.TestCheckResourceAttr("temps_otel_alert_rule.errors", "metric_name", logAlertMetricName),
		},
		{Config: cfg(9, "page"), ExpectError: regexp.MustCompile(`severity`)},
		{Config: f.config(`
resource "temps_otel_alert_rule" "other" {
  project_id       = 404
  name             = "x"
  metric_name      = "m"
  aggregation      = "avg"
  detection_config = jsonencode({ kind = "static", comparator = "gt", threshold = 1 })
  window_secs      = 60
  severity         = "info"
}`), ExpectError: regexp.MustCompile(`(?s)404.*project`)},
	}, func(*terraform.State) error {
		var n int
		f.with(func() { n = len(f.otelRules) })
		if n != 0 {
			return fmt.Errorf("%d OpenTelemetry alert rules left after destroy", n)
		}
		return nil
	})
}

func TestUnitOtelDashboard(t *testing.T) {
	f := newMoreFake(t)
	cfg := func(name, agg string) string {
		return f.config(`
resource "temps_otel_dashboard" "d" {
  project_id = 8
  name       = %q
  layout = jsonencode({
    sections = [{
      id    = "http"
      title = "HTTP"
      tiles = [{ id = "latency", title = "Latency", metric_name = "http.server.duration", aggregation = %q }]
    }]
  })
}`, name, agg)
	}
	const addr = "temps_otel_dashboard.d"
	var id int64
	runEngine(t, []resource.TestStep{
		{
			// Temps fills in tile defaults; they are not a difference.
			Config:           cfg("Service", "p95"),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: func(s *terraform.State) error {
				id = mustID(idOf(s, addr)[2:])
				return nil
			},
		},
		{
			Config:           cfg("Service health", "p99"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
			Check: func(*terraform.State) error {
				if f.count(fmt.Sprintf("PATCH /api/otel/dashboards/%d?project_id=8", id)) != 1 {
					return fmt.Errorf("update was not scoped to the project: %v", f.requests)
				}
				return nil
			},
		},
		{ResourceName: addr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"layout"}},
		{
			PreConfig: func() {
				f.with(func() { f.dashes[id]["layout"] = map[string]any{"sections": []any{}} })
			},
			Config:           cfg("Service health", "p99"),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
		},
		{
			PreConfig:        func() { f.with(func() { delete(f.dashes, id) }) },
			Config:           cfg("Service health", "p99"),
			ConfigPlanChecks: plannedCreate(addr),
		},
		{Config: cfg("Service health", "p99")},
		{
			// Temps drops tile keys it does not know, so the layout read
			// back would differ: refuse them before anything is sent.
			Config: f.config(`
resource "temps_otel_dashboard" "chart" {
  project_id = 8
  name       = "x"
  layout = jsonencode({
    sections = [{ id = "s", title = "S", tiles = [{ id = "t", metric_name = "m", aggregation = "avg", chart = "line" }] }]
  })
}`),
			ExpectError: regexp.MustCompile(`(?s)Tile 1 of section 1 has "chart"`),
		},
		{Config: f.config(`
resource "temps_otel_dashboard" "bad" {
  project_id = 8
  name       = "x"
  layout     = jsonencode({})
}`), ExpectError: regexp.MustCompile(`(?s)422.*sections`)},
	}, func(*terraform.State) error {
		var n int
		f.with(func() { n = len(f.dashes) })
		if n != 0 {
			return fmt.Errorf("%d dashboards left after destroy", n)
		}
		return nil
	})
}

// Removing a setting from an OpenTelemetry alert rule restores Temps'
// default (its PATCH leaves absent keys unchanged), and a rule switches
// between a metric and a log search in place.
func TestUnitOtelAlertRuleSettingsAndSource(t *testing.T) {
	f := newMoreFake(t)
	rule := func(body string) string {
		return f.config(`
resource "temps_otel_alert_rule" "r" {
  project_id       = 8
  name             = "Checkout"
  detection_config = jsonencode({ kind = "static", comparator = "gt", threshold = 500 })
  window_secs      = 300
  severity         = "warning"
  %s
}`, body)
	}
	const metric = `
  metric_name = "http.server.duration"
  aggregation = "p95"`
	tuned := rule(metric + `
  label_filters                  = jsonencode([["route", "/checkout"]])
  group_by                       = ["region"]
  dynamic_alerts                 = true
  max_series                     = 50
  grouped_notification_threshold = 10`)
	logs := rule(`
  aggregation = "count"
  source      = jsonencode({ kind = "logs", saved_search_id = 3 })`)
	const addr = "temps_otel_alert_rule.r"
	var id int64
	patch := func() map[string]any { return f.body(fmt.Sprintf("PATCH /api/otel/alerts/%d?project_id=8", id)) }
	runEngine(t, []resource.TestStep{
		{
			Config:           tuned,
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: func(s *terraform.State) error {
				id = mustID(idOf(s, addr)[2:])
				return nil
			},
		},
		{
			// Dropping the settings sends Temps' defaults.
			Config:           rule(metric),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(addr, "label_filters", "[]"),
				resource.TestCheckResourceAttr(addr, "group_by.#", "0"),
				resource.TestCheckResourceAttr(addr, "dynamic_alerts", "false"),
				resource.TestCheckResourceAttr(addr, "max_series", "20"),
				resource.TestCheckResourceAttr(addr, "grouped_notification_threshold", "5"),
				func(*terraform.State) error {
					b := patch()
					want := map[string]any{"label_filters": []any{}, "group_by": []any{}, "dynamic_alerts": false,
						"max_series": float64(defaultMaxSeries), "grouped_notification_threshold": float64(defaultGroupedThreshold)}
					for k, w := range want {
						if !sameJSON(b[k], w) {
							return fmt.Errorf("update sent %s = %v, want %v (body %v)", k, b[k], w, b)
						}
					}
					var r map[string]any
					f.with(func() { r = copyDoc(f.otelRules[id]) })
					if l, _ := r["label_filters"].([]any); len(l) != 0 {
						return fmt.Errorf("the rule still filters on %v", r["label_filters"])
					}
					return nil
				},
			),
		},
		{
			// A log search: metric_name is left to Temps.
			Config:           logs,
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(addr, "metric_name", logAlertMetricName),
				func(*terraform.State) error {
					if b := patch(); b["metric_name"] != nil {
						return fmt.Errorf("update sent the old metric_name: %v", b)
					}
					return nil
				},
			),
		},
		// An import converges whether the defaults are left out or spelled out.
		{ResourceName: addr, ImportState: true, ImportStateVerify: true},
		{
			// Back to a metric: removing source sends the metric source.
			Config:           rule(metric),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(addr, "metric_name", "http.server.duration"),
				resource.TestCheckResourceAttr(addr, "source", `{"kind":"metric"}`),
				func(*terraform.State) error {
					if b := patch(); !sameJSON(b["source"], map[string]any{"kind": "metric"}) {
						return fmt.Errorf("update sent source %v", b["source"])
					}
					return nil
				},
			),
		},
		{
			Config: rule(`
  metric_name = "http.server.duration"
  aggregation = "count"
  source      = jsonencode({ kind = "logs", saved_search_id = 3 })`),
			ExpectError: regexp.MustCompile(`metric_name is not used by a log alert`),
		},
		{
			Config: rule(`
  aggregation = "sum"
  source      = jsonencode({ kind = "logs", saved_search_id = 3 })`),
			ExpectError: regexp.MustCompile(`(?s)A log alert counts lines.*"sum"`),
		},
		{Config: rule(metric)},
		{ResourceName: addr, ImportState: true, ImportStateVerify: true},
		{
			Config: rule(metric + `
  source                         = jsonencode({ kind = "metric" })
  label_filters                  = jsonencode([])
  group_by                       = []
  dynamic_alerts                 = false
  max_series                     = 20
  grouped_notification_threshold = 5`),
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
		},
		{ResourceName: addr, ImportState: true, ImportStateVerify: true},
	}, func(*terraform.State) error {
		var n int
		f.with(func() { n = len(f.otelRules) })
		if n != 0 {
			return fmt.Errorf("%d OpenTelemetry alert rules left after destroy", n)
		}
		return nil
	})
}

// detection_kind follows detection_config: changing the detector updates it
// in the same apply.
func TestUnitOtelAlertRuleDetectorKind(t *testing.T) {
	f := newMoreFake(t)
	cfg := func(detector string) string {
		return f.config(`
resource "temps_otel_alert_rule" "r" {
  project_id       = 8
  name             = "Latency"
  metric_name      = "http.server.duration"
  aggregation      = "p95"
  detection_config = jsonencode(%s)
  window_secs      = 300
  severity         = "warning"
}`, detector)
	}
	const addr = "temps_otel_alert_rule.r"
	runEngine(t, []resource.TestStep{
		{
			Config:           cfg(`{ kind = "static", comparator = "gt", threshold = 500 }`),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check:            resource.TestCheckResourceAttr(addr, "detection_kind", "static"),
		},
		{
			Config:           cfg(`{ kind = "anomaly", sensitivity = 3 }`),
			ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
			Check:            resource.TestCheckResourceAttr(addr, "detection_kind", "anomaly"),
		},
		// Nothing else changed: the kind is kept, not shown as unknown.
		{
			Config:           cfg(`{ kind = "anomaly", sensitivity = 3 }`),
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
		},
	}, nil)
}
