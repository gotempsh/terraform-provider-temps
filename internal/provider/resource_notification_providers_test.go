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

// sentConfig returns the config object of the last request to req.
func (f *moreFake) sentConfig(req string) map[string]any {
	c, _ := f.body(req)["config"].(map[string]any)
	return c
}

type notifierCase struct {
	kind   string
	secret string // write-only config key
	create string // resource body
	update string // resource body after an in-place change
	// checkCreate and checkUpdate inspect the config sent.
	checkCreate func(cfg map[string]any) error
	checkUpdate func(cfg map[string]any) error
}

func testNotifier(t *testing.T, c notifierCase) {
	f := newMoreFake(t)
	typ := "temps_notification_provider_" + c.kind
	addr := typ + ".p"
	cfg := func(body string) string { return f.config("resource %q \"p\" {\n%s\n}", typ, body) }
	var id int64
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{
			{
				Config:           cfg(c.create),
				ConfigPlanChecks: emptyPlanAfterApply(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "enabled", "true"),
					func(s *terraform.State) error {
						id = mustID(idOf(s, addr))
						sent := f.sentConfig("POST /api/notification-providers/" + c.kind)
						if v, _ := sent[c.secret].(string); v == "" || v == "***" {
							return fmt.Errorf("create sent %s = %v", c.secret, sent[c.secret])
						}
						return c.checkCreate(sent)
					},
				),
			},
			{
				Config:           cfg(c.update),
				ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
				Check: func(*terraform.State) error {
					sent := f.sentConfig(fmt.Sprintf("PUT /api/notification-providers/%s/%d", c.kind, id))
					if v, _ := sent[c.secret].(string); v == "" || v == "***" {
						return fmt.Errorf("update must send the %s Terraform holds, sent %v", c.secret, sent[c.secret])
					}
					return c.checkUpdate(sent)
				},
			},
			{ResourceName: addr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{c.secret}},
			{
				// A change made in the console is detected and undone.
				PreConfig:        func() { f.with(func() { f.notifiers[id]["name"] = "renamed elsewhere" }) },
				Config:           cfg(c.update),
				ConfigPlanChecks: expectAction(addr, plancheck.ResourceActionUpdate),
			},
			{
				PreConfig:        func() { f.with(func() { delete(f.notifiers, id) }) },
				Config:           cfg(c.update),
				ConfigPlanChecks: plannedCreate(addr),
			},
			{
				// Temps now holds another kind under the ID: refuse to manage it.
				PreConfig: func() {
					f.with(func() {
						for _, p := range f.notifiers {
							p["provider_type"] = "webhook"
						}
					})
				},
				Config:      cfg(c.update),
				ExpectError: regexp.MustCompile(`(?s)has type "webhook";\s+temps_notification_provider_` + c.kind),
			},
			{
				PreConfig: func() {
					f.with(func() {
						for _, p := range f.notifiers {
							p["provider_type"] = c.kind
						}
					})
				},
				Config: cfg(c.update),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			var n int
			f.with(func() { n = len(f.notifiers) })
			if n != 0 {
				return fmt.Errorf("%d notification providers left after destroy", n)
			}
			return nil
		},
	})
}

func TestUnitNotificationProviderSlack(t *testing.T) {
	testNotifier(t, notifierCase{
		kind: "slack", secret: "webhook_url",
		create: `  name        = "alerts"
  webhook_url = "https://hooks.slack.example/services/T1/B1/one"
  channel     = "#ops"`,
		update: `  name        = "alerts"
  webhook_url = "https://hooks.slack.example/services/T1/B1/two"
  enabled     = false`,
		checkCreate: func(cfg map[string]any) error {
			if cfg["channel"] != "#ops" {
				return fmt.Errorf("create sent channel %v", cfg["channel"])
			}
			return nil
		},
		checkUpdate: func(cfg map[string]any) error {
			if v, ok := cfg["channel"]; !ok || v != nil {
				return fmt.Errorf("removing channel must send null, sent %v", cfg)
			}
			if cfg["webhook_url"] != "https://hooks.slack.example/services/T1/B1/two" {
				return fmt.Errorf("update sent webhook_url %v", cfg["webhook_url"])
			}
			return nil
		},
	})
}

func TestUnitNotificationProviderEmail(t *testing.T) {
	testNotifier(t, notifierCase{
		kind: "email", secret: "password",
		create: `  name         = "ops mail"
  smtp_host    = "smtp.example.com"
  smtp_port    = 587
  username     = "alerts"
  password     = "s3cret"
  from_address = "alerts@example.com"
  to_addresses = ["ops@example.com"]`,
		// Moving to another host: Temps refuses a masked password then, so the
		// full password must be sent.
		update: `  name                 = "ops mail"
  smtp_host            = "mail.example.net"
  smtp_port            = 465
  username             = "alerts"
  password             = "s3cret"
  from_address         = "alerts@example.com"
  from_name            = "Temps"
  to_addresses         = ["ops@example.com", "oncall@example.com"]
  tls_mode             = "Tls"
  accept_invalid_certs = true`,
		checkCreate: func(cfg map[string]any) error {
			if cfg["tls_mode"] != "Starttls" || cfg["starttls_required"] != true || cfg["accept_invalid_certs"] != false {
				return fmt.Errorf("create did not send the defaults: %v", cfg)
			}
			return nil
		},
		checkUpdate: func(cfg map[string]any) error {
			if cfg["smtp_host"] != "mail.example.net" || cfg["password"] != "s3cret" || cfg["from_name"] != "Temps" {
				return fmt.Errorf("update sent %v", cfg)
			}
			return nil
		},
	})
}

func TestUnitNotificationProviderTelegram(t *testing.T) {
	testNotifier(t, notifierCase{
		kind: "telegram", secret: "bot_token",
		create: `  name              = "chat"
  bot_token         = "123456789:AAbbCC"
  chat_id           = "-1001234567890"
  message_thread_id = 42`,
		update: `  name      = "chat"
  bot_token = "123456789:AAbbCC"
  chat_id   = "@ops_channel"`,
		checkCreate: func(cfg map[string]any) error {
			if fmt.Sprint(cfg["message_thread_id"]) != "42" {
				return fmt.Errorf("create sent message_thread_id %v", cfg["message_thread_id"])
			}
			return nil
		},
		checkUpdate: func(cfg map[string]any) error {
			if v, ok := cfg["message_thread_id"]; !ok || v != nil {
				return fmt.Errorf("removing message_thread_id must send null, sent %v", cfg)
			}
			return nil
		},
	})
}

func TestUnitNotificationProviderCloudflare(t *testing.T) {
	testNotifier(t, notifierCase{
		kind: "cloudflare", secret: "api_token",
		create: `  name         = "cf mail"
  account_id   = "0123456789abcdef"
  api_token    = "cf-token-1"
  from_address = "alerts@mail.example.com"
  to_addresses = ["ops@example.com"]`,
		update: `  name         = "cf mail"
  account_id   = "0123456789abcdef"
  api_token    = "cf-token-2"
  from_address = "alerts@mail.example.com"
  from_name    = "Temps alerts"
  to_addresses = ["ops@example.com"]`,
		checkCreate: func(map[string]any) error { return nil },
		checkUpdate: func(cfg map[string]any) error {
			if cfg["api_token"] != "cf-token-2" || cfg["from_name"] != "Temps alerts" {
				return fmt.Errorf("update sent %v", cfg)
			}
			return nil
		},
	})
}

// TestUnitNotificationProviderRejections covers an import of another kind of
// provider and a configuration Temps refuses.
func TestUnitNotificationProviderRejections(t *testing.T) {
	f := newMoreFake(t)
	var webhookID int64
	f.with(func() {
		webhookID = f.next()
		f.notifiers[webhookID] = map[string]any{"id": webhookID, "name": "hook", "provider_type": "webhook", "enabled": true,
			"config": map[string]any{"url": "https://hooks.example.com/x"}}
	})
	runEngine(t, []resource.TestStep{
		{
			Config: f.config(`
resource "temps_notification_provider_slack" "p" {
  name        = "alerts"
  webhook_url = "https://hooks.slack.example/services/T1/B1/one"
}`),
			ResourceName:  "temps_notification_provider_slack.p",
			ImportState:   true,
			ImportStateId: fmt.Sprint(webhookID),
			ExpectError:   regexp.MustCompile(`has type "webhook"`),
		},
		{
			Config: f.config(`
resource "temps_notification_provider_telegram" "p" {
  name      = "chat"
  bot_token = "not-a-token"
  chat_id   = "42"
}`),
			ExpectError: regexp.MustCompile(`(?s)400.*BotFather`),
		},
		{
			Config: f.config(`
resource "temps_notification_provider_email" "p" {
  name         = "mail"
  smtp_host    = "smtp.example.com"
  smtp_port    = 587
  username     = "u"
  password     = "p"
  from_address = "a@example.com"
  to_addresses = ["b@example.com"]
  tls_mode     = "ssl"
}`),
			ExpectError: regexp.MustCompile(`tls_mode`),
		},
	}, nil)
}
