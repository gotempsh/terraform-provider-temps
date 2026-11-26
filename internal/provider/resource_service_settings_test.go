// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

func svcSettingsConfig(h *harness, serviceType, extra string) string {
	params := `parameters = { database = "app", username = "app" }`
	if serviceType != "postgres" {
		params = ""
	}
	return h.config(`
resource "temps_service" "db" {
  name                = "db"
  service_type        = %q
  deletion_protection = false
  %s
%s
}`, serviceType, params, extra)
}

// onlyService is the ID of the single service in the fake.
func (h *harness) onlyService() int64 {
	var id int64
	for sid := range h.fake.services {
		id = sid
	}
	return id
}

// settingsCheck inspects the fake's settings of the single service.
func (h *harness) settingsCheck(fn func(id int64, st *fakeSettings) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		var err error
		h.fake.with(func() { err = fn(h.onlyService(), h.fake.st) })
		return err
	}
}

func ptrIs(p *int64, want int64) bool { return p != nil && *p == want }

func (h *harness) requestCount(prefix, suffix string) int {
	n := 0
	h.fake.with(func() {
		for _, r := range h.fake.requests {
			if strings.HasPrefix(r, prefix) && strings.HasSuffix(r, suffix) {
				n++
			}
		}
	})
	return n
}

const allServiceSettings = `
  health_check = {
    interval_secs = 30
    retries       = 5
  }
  tls_mode      = "available"
  log_ingestion = { lines_per_sec = 500 }
  workload_mode = "shadow"`

const changedServiceSettings = `
  health_check = {
    interval_secs     = 60
    timeout_secs      = 5
    apply_immediately = true
  }
  tls_mode      = "off"
  log_ingestion = {}
  workload_mode = "enforce"`

// TestUnitServiceSettings covers setting, changing, clearing, drift, import
// and unmanaging the healthcheck, TLS, log ingestion and workload mode.
func TestUnitServiceSettings(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{
		{
			Config:           svcSettingsConfig(h, "postgres", allServiceSettings),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(serviceAddr, "health_check.interval_secs", "30"),
				resource.TestCheckResourceAttr(serviceAddr, "health_check.retries", "5"),
				resource.TestCheckNoResourceAttr(serviceAddr, "health_check.timeout_secs"),
				resource.TestCheckResourceAttr(serviceAddr, "tls_mode", "available"),
				resource.TestCheckResourceAttr(serviceAddr, "log_ingestion.lines_per_sec", "500"),
				resource.TestCheckNoResourceAttr(serviceAddr, "log_ingestion.kib_per_sec"),
				resource.TestCheckResourceAttr(serviceAddr, "workload_mode", "shadow"),
				resource.TestCheckResourceAttr(serviceAddr, "status", "running"),
				h.settingsCheck(func(id int64, st *fakeSettings) error {
					o := st.health[id]
					if o == nil || !ptrIs(o.IntervalSecs, 30) || !ptrIs(o.Retries, 5) || o.TimeoutSecs != nil {
						return fmt.Errorf("healthcheck overrides on the server: %+v", o)
					}
					if st.applied[id] != 0 {
						return fmt.Errorf("the container was recreated %d times without apply_immediately", st.applied[id])
					}
					if st.tls[id] != "available" || st.workload[id] != "shadow" || !ptrIs(st.logs[id].LinesPerSec, 500) {
						return fmt.Errorf("server has tls %q, workload %q, logs %+v", st.tls[id], st.workload[id], st.logs[id])
					}
					return nil
				}),
			),
		},
		{
			ResourceName: serviceAddr, ImportState: true, ImportStateVerify: true,
			ImportStateVerifyIgnore: []string{"parameters", "deletion_protection", "timeouts"},
		},
		{
			// Change values, clear retries and the log budget, recreate now.
			Config:           svcSettingsConfig(h, "postgres", changedServiceSettings),
			ConfigPlanChecks: expectAction(serviceAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(serviceAddr, "health_check.interval_secs", "60"),
				resource.TestCheckResourceAttr(serviceAddr, "health_check.timeout_secs", "5"),
				resource.TestCheckNoResourceAttr(serviceAddr, "health_check.retries"),
				resource.TestCheckResourceAttr(serviceAddr, "health_check.apply_immediately", "true"),
				resource.TestCheckResourceAttr(serviceAddr, "tls_mode", "off"),
				resource.TestCheckNoResourceAttr(serviceAddr, "log_ingestion.lines_per_sec"),
				resource.TestCheckResourceAttr(serviceAddr, "workload_mode", "enforce"),
				resource.TestCheckResourceAttr(serviceAddr, "status", "running"),
				h.settingsCheck(func(id int64, st *fakeSettings) error {
					if o := st.health[id]; o.Retries != nil || !ptrIs(o.IntervalSecs, 60) {
						return fmt.Errorf("healthcheck overrides on the server: %+v", o)
					}
					if st.applied[id] != 1 {
						return fmt.Errorf("apply_immediately recreated the container %d times, want 1", st.applied[id])
					}
					if l := st.logs[id]; l.LinesPerSec != nil || l.KiBPerSec != nil {
						return fmt.Errorf("log budget not cleared: %+v", l)
					}
					if st.tls[id] != "off" || st.workload[id] != "enforce" {
						return fmt.Errorf("server has tls %q, workload %q", st.tls[id], st.workload[id])
					}
					return nil
				}),
			),
		},
		{
			// Changes made outside Terraform are detected and reverted.
			PreConfig: func() {
				h.fake.with(func() {
					id := h.onlyService()
					h.fake.st.health[id] = &client.HealthcheckOverrides{IntervalSecs: ptr(int64(90))}
					h.fake.st.tls[id] = "available"
					h.fake.st.logs[id] = &client.ServiceLogIngestion{KiBPerSec: ptr(int64(64))}
					h.fake.st.workload[id] = "legacy"
				})
			},
			Config:           svcSettingsConfig(h, "postgres", changedServiceSettings),
			ConfigPlanChecks: expectAction(serviceAddr, plancheck.ResourceActionUpdate),
			Check: h.settingsCheck(func(id int64, st *fakeSettings) error {
				if o := st.health[id]; !ptrIs(o.IntervalSecs, 60) || !ptrIs(o.TimeoutSecs, 5) {
					return fmt.Errorf("healthcheck drift not reverted: %+v", o)
				}
				if st.tls[id] != "off" || st.workload[id] != "enforce" || st.logs[id].KiBPerSec != nil {
					return fmt.Errorf("drift not reverted: tls %q, workload %q, logs %+v", st.tls[id], st.workload[id], st.logs[id])
				}
				return nil
			}),
		},
		{
			// Removing the arguments stops managing them: nothing is sent,
			// the service keeps its settings, and they are still shown.
			Config: svcSettingsConfig(h, "postgres", ""),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(serviceAddr, plancheck.ResourceActionUpdate)},
				PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
			},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(serviceAddr, "health_check.interval_secs", "60"),
				resource.TestCheckResourceAttr(serviceAddr, "tls_mode", "off"),
				resource.TestCheckNoResourceAttr(serviceAddr, "workload_mode"),
				h.settingsCheck(func(id int64, st *fakeSettings) error {
					if st.workload[id] != "enforce" || !ptrIs(st.health[id].IntervalSecs, 60) {
						return fmt.Errorf("unmanaging changed the server: workload %q, healthcheck %+v", st.workload[id], st.health[id])
					}
					return nil
				}),
			),
		},
	}, h.serviceDestroyed())
}

// TestUnitServiceSettingsUnsetLeavesServerAlone checks that a configuration
// without the new arguments never writes them, follows changes made in the
// console without planning a change, and never needs an administrator key.
func TestUnitServiceSettingsUnsetLeavesServerAlone(t *testing.T) {
	h := unitHarness(t)
	h.fake.st.notAdmin = true
	cfg := svcSettingsConfig(h, "postgres", "")
	noWrites := func(*terraform.State) error {
		for _, ep := range []string{"/healthcheck", "/healthcheck/apply", "/tls", "/log-ingestion", "/workload-mode"} {
			for _, m := range []string{"PUT ", "POST "} {
				if n := h.requestCount(m, ep); n > 0 {
					return fmt.Errorf("%d %s...%s requests for a configuration that does not set it", n, m, ep)
				}
			}
		}
		if n := h.requestCount("GET ", "/workload-mode"); n > 0 {
			return fmt.Errorf("read the workload mode %d times without it being configured", n)
		}
		return nil
	}
	h.run([]resource.TestStep{
		{
			Config:           cfg,
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(serviceAddr, "tls_mode", "off"),
				resource.TestCheckNoResourceAttr(serviceAddr, "health_check.interval_secs"),
				resource.TestCheckNoResourceAttr(serviceAddr, "workload_mode"),
				noWrites,
			),
		},
		{
			PreConfig: func() {
				h.fake.with(func() {
					id := h.onlyService()
					h.fake.st.health[id] = &client.HealthcheckOverrides{Retries: ptr(int64(7))}
					h.fake.st.tls[id] = "available"
				})
			},
			Config:           cfg,
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(serviceAddr, "health_check.retries", "7"),
				resource.TestCheckResourceAttr(serviceAddr, "tls_mode", "available"),
				noWrites,
			),
		},
		{
			// An import by a non-administrator leaves the workload mode unset.
			ResourceName: serviceAddr, ImportState: true, ImportStateVerify: true,
			ImportStateVerifyIgnore: []string{"parameters", "deletion_protection", "timeouts"},
		},
	}, h.serviceDestroyed())
}

// TestUnitServiceSettingsOldServer: a server without the endpoints keeps
// existing configurations working; setting one fails with a clear message.
func TestUnitServiceSettingsOldServer(t *testing.T) {
	h := unitHarness(t)
	h.fake.st.old = true
	h.run([]resource.TestStep{
		{
			Config:           svcSettingsConfig(h, "postgres", ""),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr(serviceAddr, "tls_mode"),
				resource.TestCheckNoResourceAttr(serviceAddr, "health_check.%"),
			),
		},
		{ResourceName: serviceAddr, ImportState: true, ImportStateVerify: true,
			ImportStateVerifyIgnore: []string{"parameters", "deletion_protection", "timeouts"}},
		{
			Config:      svcSettingsConfig(h, "postgres", `  log_ingestion = { lines_per_sec = 10 }`),
			ExpectError: regexp.MustCompile(`(?s)log ingestion budget.*does not provide this endpoint`),
		},
	}, h.serviceDestroyed())
}

func TestUnitServiceSettingsErrors(t *testing.T) {
	t.Run("validation", func(t *testing.T) {
		h := unitHarness(t)
		for _, tc := range []struct{ extra, want string }{
			{`  health_check = { interval_secs = 2 }`, `between 5 and 3600`},
			{`  health_check = { retries = 11 }`, `between 1 and 10`},
			{`  tls_mode = "strict"`, `value must be one of`},
			{`  log_ingestion = { kib_per_sec = -1 }`, `between 0 and 1048576`},
			{`  workload_mode = "fast"`, `value must be one of`},
		} {
			h.run([]resource.TestStep{{Config: svcSettingsConfig(h, "postgres", tc.extra), PlanOnly: true,
				ExpectError: regexp.MustCompile(tc.want)}}, nil)
		}
	})
	t.Run("healthcheck refused for an S3 store", func(t *testing.T) {
		h := unitHarness(t)
		h.run([]resource.TestStep{{
			Config:      svcSettingsConfig(h, "s3", `  health_check = { interval_secs = 30 }`),
			ExpectError: regexp.MustCompile(`(?s)Unable to set the healthcheck.*409.*no Docker healthcheck`),
		}}, h.serviceDestroyed())
	})
	t.Run("clearing is accepted for any service", func(t *testing.T) {
		h := unitHarness(t)
		h.run([]resource.TestStep{{
			Config:           svcSettingsConfig(h, "s3", `  health_check = {}`),
			ConfigPlanChecks: emptyPlanAfterApply(),
		}}, h.serviceDestroyed())
	})
	t.Run("server rejects a mode", func(t *testing.T) {
		h := unitHarness(t)
		h.run([]resource.TestStep{
			{Config: svcSettingsConfig(h, "postgres", "")},
			{
				Config:      svcSettingsConfig(h, "postgres", `  tls_mode = "required"`),
				ExpectError: regexp.MustCompile(`(?s)Unable to change the TLS mode.*to required.*not supported yet`),
			},
		}, h.serviceDestroyed())
	})
	t.Run("apply refused", func(t *testing.T) {
		h := unitHarness(t)
		h.run([]resource.TestStep{
			{Config: svcSettingsConfig(h, "postgres", "")},
			{
				PreConfig: func() {
					h.fake.fail(fmt.Sprintf("POST /api/external-services/%d/healthcheck/apply", h.onlyService()), 409,
						"Healthcheck Not Available For This Service", "it runs on worker node 2")
				},
				Config:      svcSettingsConfig(h, "postgres", `  health_check = { retries = 3, apply_immediately = true }`),
				ExpectError: regexp.MustCompile(`(?s)Saved the healthcheck.*could not recreate.*worker node 2`),
			},
		}, h.serviceDestroyed())
	})
	t.Run("workload mode needs an administrator", func(t *testing.T) {
		h := unitHarness(t)
		h.fake.st.notAdmin = true
		h.run([]resource.TestStep{
			{Config: svcSettingsConfig(h, "postgres", "")},
			{
				Config:      svcSettingsConfig(h, "postgres", `  workload_mode = "shadow"`),
				ExpectError: regexp.MustCompile(`(?s)workload mode.*instance\s+administrator.*403`),
			},
		}, h.serviceDestroyed())
	})
	t.Run("read error", func(t *testing.T) {
		h := unitHarness(t)
		h.run([]resource.TestStep{
			{Config: svcSettingsConfig(h, "postgres", "")},
			{
				PreConfig: func() {
					h.fake.fail(fmt.Sprintf("GET /api/external-services/%d/tls", h.onlyService()), 500, "Internal Server Error", "inspect failed")
				},
				Config:      svcSettingsConfig(h, "postgres", ""),
				ExpectError: regexp.MustCompile(`(?s)Unable to read the TLS mode.*inspect failed`),
			},
		}, h.serviceDestroyed())
	})
}

// Changing the TLS mode waits for the database to come back.
func TestUnitServiceTLSRestartWaits(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{
		{Config: svcSettingsConfig(h, "postgres", "")},
		{
			Config: svcSettingsConfig(h, "postgres", `  tls_mode = "available"`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(serviceAddr, "status", "running"),
				h.settingsCheck(func(id int64, st *fakeSettings) error {
					if st.tlsSets[id] != 1 {
						return fmt.Errorf("TLS mode set %d times, want 1", st.tlsSets[id])
					}
					return nil
				}),
			),
			ConfigPlanChecks: expectAction(serviceAddr, plancheck.ResourceActionUpdate),
		},
	}, h.serviceDestroyed())
}

// Overrides Temps saved but has not applied yet are recreated by an apply
// with apply_immediately, even when they already match the configuration.
func TestUnitServiceHealthcheckPendingRecreate(t *testing.T) {
	h := unitHarness(t)
	applied := func(want int) resource.TestCheckFunc {
		return h.settingsCheck(func(id int64, st *fakeSettings) error {
			if st.applied[id] != want {
				return fmt.Errorf("healthcheck applied %d times, want %d", st.applied[id], want)
			}
			return nil
		})
	}
	h.run([]resource.TestStep{
		{
			Config:           svcSettingsConfig(h, "postgres", `  health_check = { retries = 3, apply_immediately = true }`),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check:            applied(1),
		},
		{
			// A recreate is pending (an earlier one failed, or the overrides
			// were saved again elsewhere): the next apply recreates.
			PreConfig:        func() { h.fake.with(func() { h.fake.st.applied[h.onlyService()] = 0 }) },
			Config:           svcSettingsConfig(h, "postgres", `  health_check = { retries = 3, apply_immediately = true }`),
			ConfigPlanChecks: expectAction(serviceAddr, plancheck.ResourceActionUpdate),
			Check:            applied(1),
		},
		{
			Config:           svcSettingsConfig(h, "postgres", `  health_check = { retries = 4, apply_immediately = false }`),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check:            applied(0),
		},
		{
			// Turning apply_immediately on applies what is pending.
			Config:           svcSettingsConfig(h, "postgres", `  health_check = { retries = 4, apply_immediately = true }`),
			ConfigPlanChecks: expectAction(serviceAddr, plancheck.ResourceActionUpdate),
			Check:            applied(1),
		},
	}, h.serviceDestroyed())
}

// A setting refused while creating a protected service leaves it tainted;
// both the error and the refused replacement say how to keep it.
func TestUnitServiceCreateRefusedSettingProtected(t *testing.T) {
	h := unitHarness(t)
	svc := func(extra string) string {
		return h.config(`
resource "temps_service" "db" {
  name         = "db"
  service_type = "s3"
  %s
}`, extra)
	}
	h.run([]resource.TestStep{
		{
			Config:      svc(`health_check = { interval_secs = 30 }`),
			ExpectError: regexp.MustCompile(`(?s)Unable to set the healthcheck.*no Docker healthcheck.*was created.*terraform\s+untaint`),
		},
		{
			Config:      svc(""),
			ExpectError: regexp.MustCompile(`(?s)protected against deletion.*tainted.*terraform\s+untaint`),
		},
		{
			Config: h.config(`
removed {
  from = temps_service.db
  lifecycle {
    destroy = false
  }
}`),
		},
	}, nil)
}
