// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"fmt"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const deploymentAddr = "temps_deployment.web"

func deploymentConfig(h *harness, args string) string {
	return h.config(h.projectConfig("deploy")+`
resource "temps_deployment" "web" {
  project_id     = temps_project.parent.id
  environment_id = data.temps_environment.production.id
%s
}`, args)
}

// deployments returns the fake's deployments, oldest first.
func (h *harness) deployments() []fakeDeployment {
	var out []fakeDeployment
	h.fake.with(func() {
		for _, fd := range h.fake.dep.byID {
			out = append(out, *fd)
		}
	})
	slices.SortFunc(out, func(a, b fakeDeployment) int { return int(a.d.ID - b.d.ID) })
	return out
}

// wantDeployments checks how many deployments Temps has, and runs fn on the
// newest.
func (h *harness) wantDeployments(n int, fn func(newest fakeDeployment) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		ds := h.deployments()
		if len(ds) != n {
			return fmt.Errorf("Temps has %d deployments, want %d", len(ds), n)
		}
		if fn == nil {
			return nil
		}
		return fn(ds[n-1])
	}
}

func TestUnitDeployment(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{
		{
			Config: deploymentConfig(h, `  image             = "ghcr.io/example-org/app:1.0.0"
  command           = ["/app", "--port", "8080"]
  health_check_path = "/healthz"`),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(deploymentAddr, "status", "completed"),
				resource.TestCheckResourceAttr(deploymentAddr, "is_current", "true"),
				resource.TestCheckResourceAttr(deploymentAddr, "finished_at", "2026-01-01T00:01:00Z"),
				resource.TestCheckResourceAttrSet(deploymentAddr, "url"),
				h.wantDeployments(1, func(d fakeDeployment) error {
					if d.req.ImageRef != "ghcr.io/example-org/app:1.0.0" || !slices.Equal(d.req.Command, []string{"/app", "--port", "8080"}) ||
						d.req.HealthCheckPath == nil || *d.req.HealthCheckPath != "/healthz" {
						return fmt.Errorf("deploy request was %+v", d.req)
					}
					return nil
				}),
			),
		},
		{
			// A new image deploys again; the old deployment is not torn down,
			// it is replaced by the new one.
			Config:           deploymentConfig(h, `  image = "ghcr.io/example-org/app:1.1.0"`),
			ConfigPlanChecks: expectAction(deploymentAddr, plancheck.ResourceActionDestroyBeforeCreate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(deploymentAddr, "is_current", "true"),
				resource.TestCheckNoResourceAttr(deploymentAddr, "command"),
				h.wantDeployments(2, func(d fakeDeployment) error {
					if d.req.Command != nil || d.req.HealthCheckPath != nil {
						return fmt.Errorf("left-out command and health check path were sent: %+v", d.req)
					}
					if first := h.deployments()[0]; first.d.Status != "completed" || first.d.IsCurrent {
						return fmt.Errorf("previous deployment is %s, current=%t", first.d.Status, first.d.IsCurrent)
					}
					return nil
				}),
			),
		},
		{
			// wait_for_completion changes in place and deploys nothing.
			Config: deploymentConfig(h, `  image               = "ghcr.io/example-org/app:1.1.0"
  wait_for_completion = false`),
			ConfigPlanChecks: expectAction(deploymentAddr, plancheck.ResourceActionUpdate),
			Check:            h.wantDeployments(2, nil),
		},
		{
			// triggers redeploy the same image.
			Config: deploymentConfig(h, `  image    = "ghcr.io/example-org/app:1.1.0"
  triggers = { build = "42" }`),
			ConfigPlanChecks: expectAction(deploymentAddr, plancheck.ResourceActionDestroyBeforeCreate),
			Check:            h.wantDeployments(3, nil),
		},
		{
			// A deployment made elsewhere shows as no longer current and is
			// not undone.
			PreConfig: func() {
				h.fake.with(func() {
					for _, fd := range h.fake.dep.byID {
						fd.d.IsCurrent = false
					}
				})
			},
			Config: deploymentConfig(h, `  image    = "ghcr.io/example-org/app:1.1.0"
  triggers = { build = "42" }`),
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(deploymentAddr, "is_current", "false"),
				h.wantDeployments(3, nil),
			),
		},
	}, func(*terraform.State) error {
		// Destroying leaves every deployment in Temps.
		for _, d := range h.deployments() {
			if d.d.Status != "completed" {
				return fmt.Errorf("deployment %d is %s after destroy, want it left completed", d.d.ID, d.d.Status)
			}
		}
		return nil
	})
}

func TestUnitDeploymentNoWait(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: deploymentConfig(h, `  image               = "ghcr.io/example-org/app:slow"
  wait_for_completion = false`),
		Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(deploymentAddr, "status", "running"),
			h.wantDeployments(1, nil),
		),
	}}, nil)
}

// A failed deployment reports Temps' cause, stays in state tainted, and the
// next apply deploys again.
func TestUnitDeploymentFailed(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{
		{
			Config:      deploymentConfig(h, `  image = "ghcr.io/example-org/app:broken"`),
			ExpectError: regexp.MustCompile(`(?s)deployment \d+ .* failed.*Image tag not found \(stage pull, code\s+image_not_found, job pull_image\)\.\s+Push\s+the tag.*tainted`),
		},
		{
			Config:           deploymentConfig(h, `  image = "ghcr.io/example-org/app:1.0.0"`),
			ConfigPlanChecks: expectAction(deploymentAddr, plancheck.ResourceActionDestroyBeforeCreate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(deploymentAddr, "status", "completed"),
				h.wantDeployments(2, nil),
			),
		},
	}, nil)
}

// A deployment Terraform stops waiting for is cancelled, not left to switch
// the environment later.
func TestUnitDeploymentTimeoutCancels(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: deploymentConfig(h, `  image = "ghcr.io/example-org/app:slow"
  timeouts {
    create = "200ms"
  }`),
		ExpectError: regexp.MustCompile(`(?s)did not complete.*timed out waiting.*Terraform cancelled deployment \d+`),
	}, {
		// It was cancelled, and being tainted it is deployed again.
		PreConfig: func() {
			if ds := h.deployments(); len(ds) != 1 || ds[0].d.Status != "cancelled" {
				t.Errorf("after the timeout Temps has %+v, want one cancelled deployment", ds)
			}
		},
		Config:           deploymentConfig(h, `  image = "ghcr.io/example-org/app:1.0.0"`),
		ConfigPlanChecks: expectAction(deploymentAddr, plancheck.ResourceActionDestroyBeforeCreate),
		Check:            h.wantDeployments(2, nil),
	}}, nil)
}

// A deployment that completes just as Terraform gives up on it is kept, not
// tainted and deployed again.
func TestUnitDeploymentCompletesAsTimeoutFires(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: deploymentConfig(h, `  image = "ghcr.io/example-org/app:late"
  timeouts {
    create = "200ms"
  }`),
		ConfigPlanChecks: emptyPlanAfterApply(),
		Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(deploymentAddr, "status", "completed"),
			resource.TestCheckResourceAttr(deploymentAddr, "is_current", "true"),
			resource.TestCheckResourceAttr(deploymentAddr, "finished_at", "2026-01-01T00:01:00Z"),
			h.wantDeployments(1, nil),
		),
	}}, nil)
}

// A deployment Terraform cannot follow because a status read failed is
// cancelled too, not left to switch the environment after a failed apply.
func TestUnitDeploymentPollErrorCancels(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config:      deploymentConfig(h, `  image = "ghcr.io/example-org/app:unreadable"`),
		ExpectError: regexp.MustCompile(`(?s)Unable to follow the deployment \d+.*A\s+database\s+error\s+occurred.*Terraform\s+cancelled\s+deployment\s+\d+`),
	}, {
		PreConfig: func() {
			if ds := h.deployments(); len(ds) != 1 || ds[0].d.Status != "cancelled" {
				t.Errorf("after the failed read Temps has %+v, want one cancelled deployment", ds)
			}
		},
		Config:           deploymentConfig(h, `  image = "ghcr.io/example-org/app:1.0.0"`),
		ConfigPlanChecks: expectAction(deploymentAddr, plancheck.ResourceActionDestroyBeforeCreate),
		Check:            h.wantDeployments(2, nil),
	}}, nil)
}

// A deployment Temps no longer has is deployed again.
func TestUnitDeploymentGone(t *testing.T) {
	h := unitHarness(t)
	cfg := deploymentConfig(h, `  image = "ghcr.io/example-org/app:1.0.0"`)
	h.run([]resource.TestStep{
		{Config: cfg},
		{
			PreConfig:        func() { h.fake.with(func() { clear(h.fake.dep.byID) }) },
			Config:           cfg,
			ConfigPlanChecks: expectAction(deploymentAddr, plancheck.ResourceActionCreate),
			Check:            h.wantDeployments(1, nil),
		},
	}, nil)
}

func TestUnitDeploymentErrors(t *testing.T) {
	t.Run("validation", func(t *testing.T) {
		h := unitHarness(t)
		for _, tc := range []struct{ args, want string }{
			{`  image = "ghcr.io/example-org/my app:1"`, `must not be empty or contain spaces`},
			{`  image = ""`, `must not be empty or contain spaces`},
			{"  image = \"app:1\"\n  health_check_path = \"healthz\"", `must start with '/'`},
			{"  image = \"app:1\"\n  command = []", `list must contain at least 1`},
		} {
			h.run([]resource.TestStep{{Config: deploymentConfig(h, tc.args), PlanOnly: true, ExpectError: regexp.MustCompile(tc.want)}}, nil)
		}
	})
	t.Run("refused by the server", func(t *testing.T) {
		h := unitHarness(t)
		h.run([]resource.TestStep{{Config: h.config(h.projectConfig("deploy"))}, {
			PreConfig: func() {
				h.fake.with(func() {
					for id := range h.fake.envs {
						h.fake.failures[fmt.Sprintf("POST /api/projects/%d/environments/%d/deploy/image", h.fake.envs[id].ProjectID, id)] =
							fakeFailure{status: 403, body: `{"title":"Insufficient Permissions","detail":"needs deployments:create"}`}
					}
				})
			},
			Config:      deploymentConfig(h, `  image = "ghcr.io/example-org/app:1.0.0"`),
			ExpectError: regexp.MustCompile(`(?s)Unable to start the deployment of ghcr.io/example-org/app:1.0.0.*deployments:read,\s+deployments:create`),
		}}, nil)
	})
}
