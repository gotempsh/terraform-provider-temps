// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const (
	localhostFilterAddr = "temps_error_inbound_filter.localhost"
	messageFilterAddr   = "temps_error_inbound_filter.noise"
)

func inboundFilterConfig(h *harness, messageBlock string) string {
	return h.config(h.projectConfig("filters") + `
resource "temps_error_inbound_filter" "localhost" {
  project_id = temps_project.parent.id
  kind       = "localhost"
}

resource "temps_error_inbound_filter" "noise" {
  project_id = temps_project.parent.id
` + messageBlock + `
}`)
}

func inboundFilterSteps(h *harness) []resource.TestStep {
	return []resource.TestStep{
		{
			Config:           inboundFilterConfig(h, `  kind = "error_message"`+"\n"+`  pattern = "*ResizeObserver loop*"`),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(localhostFilterAddr, "enabled", "true"),
				resource.TestCheckNoResourceAttr(localhostFilterAddr, "pattern"),
				resource.TestCheckResourceAttr(messageFilterAddr, "pattern", "*ResizeObserver loop*"),
				resource.TestCheckResourceAttr(messageFilterAddr, "dropped_count", "0"),
			),
		},
		// The pattern and the switch change in place.
		{
			Config:           inboundFilterConfig(h, `  kind = "error_message"`+"\n"+`  pattern = "ChunkLoadError: *"`+"\n"+`  enabled = false`),
			ConfigPlanChecks: expectAction(messageFilterAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(messageFilterAddr, "pattern", "ChunkLoadError: *"),
				resource.TestCheckResourceAttr(messageFilterAddr, "enabled", "false"),
			),
		},
		// A different kind is a different rule.
		{
			Config:           inboundFilterConfig(h, `  kind = "release"`+"\n"+`  pattern = "*-canary"`),
			ConfigPlanChecks: expectAction(messageFilterAddr, plancheck.ResourceActionDestroyBeforeCreate),
			Check:            resource.TestCheckResourceAttr(messageFilterAddr, "kind", "release"),
		},
		{ResourceName: localhostFilterAddr, ImportState: true, ImportStateVerify: true, ImportStateIdFunc: stateID(localhostFilterAddr, "project_id", "id")},
		{ResourceName: messageFilterAddr, ImportState: true, ImportStateVerify: true, ImportStateIdFunc: stateID(messageFilterAddr, "project_id", "id")},
	}
}

func (h *harness) inboundFiltersDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_error_inbound_filter", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetInboundFilter(context.Background(), mustID(rs.Primary.Attributes["project_id"]), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitInboundFilter(t *testing.T) {
	h := unitHarness(t)
	h.run(inboundFilterSteps(h), h.inboundFiltersDestroyed())
}

func TestAccInboundFilter(t *testing.T) {
	h := accHarness(t)
	h.run(inboundFilterSteps(h), h.inboundFiltersDestroyed())
}

// A second built-in filter of the same kind is a 409 that points at import.
func TestUnitInboundFilterDuplicateBuiltIn(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: h.config(h.projectConfig("filters-dup") + `
resource "temps_error_inbound_filter" "a" {
  project_id = temps_project.parent.id
  kind       = "web_crawlers"
}

resource "temps_error_inbound_filter" "b" {
  project_id = temps_project.parent.id
  kind       = "web_crawlers"
  depends_on = [temps_error_inbound_filter.a]
}`),
		ExpectError: regexp.MustCompile(`already has a "web_crawlers" filter`),
	}}, nil)
}

func TestUnitInboundFilterValidation(t *testing.T) {
	h := unitHarness(t)
	filter := func(body string) string {
		return h.config(`resource "temps_error_inbound_filter" "f" {` + "\n  project_id = 1\n" + body + "\n}")
	}
	h.run([]resource.TestStep{
		{Config: filter(`  kind = "release"`), ExpectError: regexp.MustCompile(`needs a pattern`)},
		{Config: filter(`  kind = "localhost"` + "\n" + `  pattern = "x"`), ExpectError: regexp.MustCompile(`takes no pattern`)},
		{Config: filter(`  kind = "error_message"` + "\n" + `  pattern = "  "`), ExpectError: regexp.MustCompile(`non-space`)},
		{Config: filter(`  kind = "error_message"` + "\n" + `  pattern = "` + strings.Repeat("é", 201) + `"`), ExpectError: regexp.MustCompile(`201 characters`)},
		{Config: filter(`  kind = "sentry"`), ExpectError: regexp.MustCompile(`value must be one of`)},
	}, nil)
}

// A pattern of only `*` is valid on the server but drops every event.
func TestInboundFilterWarnsWhenPatternMatchesEverything(t *testing.T) {
	ctx := context.Background()
	r := &inboundFilterResource{}
	var sr fwresource.SchemaResponse
	r.Schema(ctx, fwresource.SchemaRequest{}, &sr)
	validate := func(kind, pattern string) diag.Diagnostics {
		typ := sr.Schema.Type().TerraformType(ctx)
		raw := tftypes.NewValue(typ, map[string]tftypes.Value{
			"id":            tftypes.NewValue(tftypes.String, nil),
			"project_id":    tftypes.NewValue(tftypes.Number, 1),
			"kind":          tftypes.NewValue(tftypes.String, kind),
			"pattern":       tftypes.NewValue(tftypes.String, pattern),
			"enabled":       tftypes.NewValue(tftypes.Bool, nil),
			"dropped_count": tftypes.NewValue(tftypes.Number, nil),
		})
		var resp fwresource.ValidateConfigResponse
		r.ValidateConfig(ctx, fwresource.ValidateConfigRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: raw}}, &resp)
		return resp.Diagnostics
	}
	for _, p := range []string{"*", "**"} {
		d := validate("release", p)
		if d.HasError() || d.WarningsCount() != 1 || !strings.Contains(d.Warnings()[0].Detail(), "drops every error event") {
			t.Errorf("pattern %q: want one match-everything warning, got %v", p, d)
		}
	}
	if d := validate("release", "*-canary"); len(d) != 0 {
		t.Errorf("pattern *-canary: want no diagnostics, got %v", d)
	}
}
