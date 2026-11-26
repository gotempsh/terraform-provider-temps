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

const tlsDomainAddr = "temps_domain.app"

func tlsDomainConfig(h *harness, settings string) string {
	return h.config(`
resource "temps_domain" "app" {
  domain         = "app.example.com"
  challenge_type = "dns-01"
%s
}`, settings)
}

// certCheck inspects the certificate settings the fake stores for the only domain.
func (h *harness) certCheck(fn func(saved map[string]any) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		var err error
		h.fake.with(func() {
			for id := range h.fake.st.certDomains {
				err = fn(h.fake.st.certs[id])
			}
		})
		return err
	}
}

func (h *harness) setCert(key string, v any) func() {
	return func() {
		h.fake.with(func() {
			for id := range h.fake.st.certDomains {
				if h.fake.st.certs[id] == nil {
					h.fake.st.certs[id] = map[string]any{}
				}
				h.fake.st.certs[id][key] = v
			}
		})
	}
}

func (h *harness) tlsDomainDestroyed() resource.TestCheckFunc {
	return func(*terraform.State) error {
		var n int
		h.fake.with(func() { n = len(h.fake.st.certDomains) })
		if n > 0 {
			return fmt.Errorf("%d domains left after destroy", n)
		}
		return nil
	}
}

func TestUnitDomainCertificateSettings(t *testing.T) {
	h := unitHarness(t)
	puts := func() int { return h.requestCount("PUT ", "/certificate-settings") }
	wantPuts := func(n int) resource.TestCheckFunc {
		return func(*terraform.State) error {
			if got := puts(); got != n {
				return fmt.Errorf("%d certificate settings writes, want %d", got, n)
			}
			return nil
		}
	}
	h.run([]resource.TestStep{
		{
			// Left out: nothing is written, the defaults are shown.
			Config:           tlsDomainConfig(h, ""),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(tlsDomainAddr, "certificate_settings.renewal_days", "30"),
				resource.TestCheckNoResourceAttr(tlsDomainAddr, "certificate_settings.alert_days"),
				resource.TestCheckNoResourceAttr(tlsDomainAddr, "certificate_settings.alerts_enabled"),
				wantPuts(0),
			),
		},
		{ResourceName: tlsDomainAddr, ImportState: true, ImportStateVerify: true},
		{
			Config: tlsDomainConfig(h, `  certificate_settings = {
    renewal_days   = 15
    alert_days     = 10
    alerts_enabled = false
  }`),
			ConfigPlanChecks: expectAction(tlsDomainAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(tlsDomainAddr, "certificate_settings.renewal_days", "15"),
				resource.TestCheckResourceAttr(tlsDomainAddr, "certificate_settings.alert_days", "10"),
				resource.TestCheckResourceAttr(tlsDomainAddr, "certificate_settings.alerts_enabled", "false"),
				h.certCheck(func(saved map[string]any) error {
					if saved["challenge_type"] != "dns-01" || saved["renewal_days"] != float64(15) || saved["alert_days"] != float64(10) || saved["alerts_enabled"] != false {
						return fmt.Errorf("saved settings %v", saved)
					}
					return nil
				}),
			),
		},
		{ResourceName: tlsDomainAddr, ImportState: true, ImportStateVerify: true},
		{
			// Values left out of the object return to their defaults.
			Config:           tlsDomainConfig(h, `  certificate_settings = { alert_days = 5 }`),
			ConfigPlanChecks: expectAction(tlsDomainAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(tlsDomainAddr, "certificate_settings.renewal_days", "30"),
				resource.TestCheckResourceAttr(tlsDomainAddr, "certificate_settings.alert_days", "5"),
				resource.TestCheckNoResourceAttr(tlsDomainAddr, "certificate_settings.alerts_enabled"),
				h.certCheck(func(saved map[string]any) error {
					if saved["alerts_enabled"] != nil || saved["renewal_days"] != float64(30) {
						return fmt.Errorf("saved settings %v", saved)
					}
					return nil
				}),
			),
		},
		{
			// A change made outside Terraform is reverted.
			PreConfig:        h.setCert("renewal_days", 60),
			Config:           tlsDomainConfig(h, `  certificate_settings = { alert_days = 5 }`),
			ConfigPlanChecks: expectAction(tlsDomainAddr, plancheck.ResourceActionUpdate),
			Check: h.certCheck(func(saved map[string]any) error {
				if saved["renewal_days"] != float64(30) {
					return fmt.Errorf("drift not reverted: %v", saved)
				}
				return nil
			}),
		},
		{
			Config:           tlsDomainConfig(h, `  certificate_settings = {}`),
			ConfigPlanChecks: expectAction(tlsDomainAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr(tlsDomainAddr, "certificate_settings.alert_days"),
				h.certCheck(func(saved map[string]any) error {
					if saved["alert_days"] != nil || saved["renewal_days"] != float64(30) {
						return fmt.Errorf("not reset: %v", saved)
					}
					return nil
				}),
				wantPuts(4),
			),
		},
		{
			// Removed from configuration: no longer managed, still shown,
			// and console changes do not plan a change.
			PreConfig:        h.setCert("alert_days", 20),
			Config:           tlsDomainConfig(h, ""),
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(tlsDomainAddr, "certificate_settings.alert_days", "20"),
				wantPuts(4),
			),
		},
		{
			// Other arguments still replace the domain.
			Config: h.config(`
resource "temps_domain" "app" {
  domain         = "www.example.com"
  challenge_type = "dns-01"
  certificate_settings = { renewal_days = 20 }
}`),
			ConfigPlanChecks: expectAction(tlsDomainAddr, plancheck.ResourceActionReplace),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(tlsDomainAddr, "domain", "www.example.com"),
				resource.TestCheckResourceAttr(tlsDomainAddr, "certificate_settings.renewal_days", "20"),
			),
		},
	}, h.tlsDomainDestroyed())
}

func TestUnitDomainCertificateSettingsErrors(t *testing.T) {
	t.Run("validation", func(t *testing.T) {
		h := unitHarness(t)
		h.run([]resource.TestStep{
			{Config: tlsDomainConfig(h, `  certificate_settings = { renewal_days = 0 }`), PlanOnly: true,
				ExpectError: regexp.MustCompile(`between 1 and 90`)},
			{Config: tlsDomainConfig(h, `  certificate_settings = { alert_days = 91 }`), PlanOnly: true,
				ExpectError: regexp.MustCompile(`between 1 and 90`)},
		}, nil)
	})
	t.Run("refused by the server", func(t *testing.T) {
		h := unitHarness(t)
		h.run([]resource.TestStep{
			{Config: tlsDomainConfig(h, "")},
			{
				PreConfig: func() {
					h.fake.fail("PUT /api/domains/1/certificate-settings", 400, "Invalid Domain", "wildcards require dns-01")
				},
				Config:      tlsDomainConfig(h, `  certificate_settings = { renewal_days = 10 }`),
				ExpectError: regexp.MustCompile(`(?s)Unable to save the certificate settings.*wildcards\s+require dns-01`),
			},
		}, h.tlsDomainDestroyed())
	})
	t.Run("read error", func(t *testing.T) {
		h := unitHarness(t)
		h.run([]resource.TestStep{
			{Config: tlsDomainConfig(h, "")},
			{
				PreConfig: func() {
					h.fake.fail("GET /api/domains/1/certificate-settings", 403, "Forbidden", "missing domains:read")
				},
				Config:      tlsDomainConfig(h, ""),
				ExpectError: regexp.MustCompile(`(?s)Unable to read the certificate settings.*needs these permissions`),
			},
		}, h.tlsDomainDestroyed())
	})
	t.Run("old server", func(t *testing.T) {
		h := unitHarness(t)
		h.fake.st.old = true
		h.run([]resource.TestStep{
			{
				Config:           tlsDomainConfig(h, ""),
				ConfigPlanChecks: emptyPlanAfterApply(),
				Check:            resource.TestCheckNoResourceAttr(tlsDomainAddr, "certificate_settings.%"),
			},
			{
				Config:      tlsDomainConfig(h, `  certificate_settings = { renewal_days = 10 }`),
				ExpectError: regexp.MustCompile(`(?s)certificate settings.*does not provide this endpoint`),
			},
		}, h.tlsDomainDestroyed())
	})
}
