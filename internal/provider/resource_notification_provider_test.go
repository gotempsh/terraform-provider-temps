// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

const notifierAddr = "temps_notification_provider.alerts"

func notifierConfig(h *harness, url, token string) string {
	return h.config(`
resource "temps_notification_provider" "alerts" {
  name         = %q
  url          = %q
  timeout_secs = 10
  headers = {
    Authorization = %q
    X-Team        = "platform"
  }
}`, h.name("alerts"), url, token)
}

func notifierSteps(h *harness) []resource.TestStep {
	return []resource.TestStep{
		{
			Config:           notifierConfig(h, "https://hooks.example.com/a", "Bearer one"),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(notifierAddr, "method", "POST"),
				resource.TestCheckResourceAttr(notifierAddr, "enabled", "true"),
				resource.TestCheckResourceAttr(notifierAddr, "timeout_secs", "10"),
				resource.TestCheckResourceAttr(notifierAddr, "headers.Authorization", "Bearer one"),
			),
		},
		// Only a header value changes: the URL is sent masked and kept.
		{
			Config:           notifierConfig(h, "https://hooks.example.com/a", "Bearer two"),
			ConfigPlanChecks: expectAction(notifierAddr, plancheck.ResourceActionUpdate),
			Check:            resource.TestCheckResourceAttr(notifierAddr, "headers.Authorization", "Bearer two"),
		},
		// The URL changes: every header must go in plaintext or Temps refuses.
		{
			Config:           notifierConfig(h, "https://hooks.example.com/b", "Bearer two"),
			ConfigPlanChecks: expectAction(notifierAddr, plancheck.ResourceActionUpdate),
			Check:            resource.TestCheckResourceAttr(notifierAddr, "url", "https://hooks.example.com/b"),
		},
		{
			ResourceName: notifierAddr, ImportState: true, ImportStateVerify: true,
			ImportStateVerifyIgnore: []string{"url", "headers"},
		},
	}
}

func (h *harness) notifiersDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_notification_provider", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetNotificationProvider(context.Background(), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitNotificationProvider(t *testing.T) {
	h := unitHarness(t)
	h.run(notifierSteps(h), h.notifiersDestroyed())
}

func TestAccNotificationProvider(t *testing.T) {
	h := accHarness(t)
	h.run(notifierSteps(h), h.notifiersDestroyed())
}

func TestUnitNotificationProviderRejectsBadURL(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: h.config(`
resource "temps_notification_provider" "alerts" {
  name = "x"
  url  = "ftp://hooks.example.com"
}`),
		ExpectError: regexp.MustCompile(`(?i)http`),
	}}, nil)
}

func TestNotificationProviderRequestMasksUnchanged(t *testing.T) {
	ctx := context.Background()
	hdr := func(m map[string]string) types.Map {
		v := map[string]attr.Value{}
		for k, s := range m {
			v[k] = types.StringValue(s)
		}
		return types.MapValueMust(types.StringType, v)
	}
	prior := &notificationProviderModel{URL: types.StringValue("https://a"), Headers: hdr(map[string]string{"A": "1", "B": "2"})}
	plan := notificationProviderModel{URL: types.StringValue("https://a"), Headers: hdr(map[string]string{"A": "1", "B": "3"})}

	body, err := plan.request(ctx, prior)
	if err != nil {
		t.Fatal(err)
	}
	if body.Config.URL != client.MaskedSecret || body.Config.Headers["A"] != client.MaskedSecret || body.Config.Headers["B"] != "3" {
		t.Fatalf("unchanged URL and header should be masked, changed header sent: %+v", body.Config)
	}

	plan.URL = types.StringValue("https://b")
	body, err = plan.request(ctx, prior)
	if err != nil {
		t.Fatal(err)
	}
	if body.Config.URL != "https://b" || body.Config.Headers["A"] != "1" {
		t.Fatalf("a URL change must send every header in plaintext: %+v", body.Config)
	}

	body, err = plan.request(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if body.Config.Headers["A"] != "1" {
		t.Fatalf("without prior state every value is sent: %+v", body.Config)
	}
}

func TestReconcileHeaders(t *testing.T) {
	got := reconcileHeaders(map[string]string{"A": "1", "Gone": "x"}, map[string]string{"A": "***", "New": "***"})
	m := got.Elements()
	if len(m) != 2 || m["A"].(types.String).ValueString() != "1" || m["New"].(types.String).ValueString() != client.MaskedSecret {
		t.Fatalf("unexpected reconcile result: %v", got)
	}
	if !reconcileHeaders(nil, nil).IsNull() {
		t.Fatal("no headers anywhere should stay null")
	}
	if n := len(reconcileHeaders(map[string]string{}, nil).Elements()); n != 0 {
		t.Fatalf("empty configured map should stay empty, got %d", n)
	}
}
