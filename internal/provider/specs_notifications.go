// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

// Notification providers other than webhooks (temps_notification_provider).
// Each kind has its own create and update endpoints and shares the read and
// delete of /notification-providers/{id}. Temps masks every credential on read
// ("***"), so credentials are write-only here: they are sent in full on every
// update, which also satisfies Temps' rule that a provider moved to a new
// destination must be given its credentials again.

// notificationProviderSpec builds the spec of one provider kind.
func notificationProviderSpec(kind, desc string, config []gfield) *gspec {
	name := "notification_provider_" + kind
	fields := []gfield{
		{Name: "name", Kind: kString, Required: true, Desc: "Display name."},
		{Name: "enabled", Kind: kBool, Optional: true, Default: true, Desc: "Send notifications through the provider."},
	}
	for _, f := range config {
		f.JSON = "config." + f.Name
		if f.Sensitive {
			f.Read = "-"
		}
		fields = append(fields, f)
	}
	return &gspec{
		Name: name,
		Desc: desc + " Temps never returns the provider's credentials, so changes made to them outside Terraform " +
			"are not detected. Reading a provider of another kind is an error.",
		Perms:    notificationProviderPermissions,
		Fields:   fields,
		Create:   gop{Method: http.MethodPost, Path: "/notification-providers/" + kind},
		Read:     gop{Path: "/notification-providers/{id}"},
		Update:   gop{Method: http.MethodPut, Path: "/notification-providers/" + kind + "/{id}"},
		Delete:   gop{Method: http.MethodDelete, Path: "/notification-providers/{id}"},
		ReadHook: readNotificationProvider(kind, name),
	}
}

// readNotificationProvider reads a provider and refuses one of another kind:
// the kind's update endpoint would otherwise overwrite it with this kind's
// configuration.
func readNotificationProvider(kind, name string) func(context.Context, *client.Client, gvals, string) (map[string]any, error) {
	return func(ctx context.Context, c *client.Client, _ gvals, id string) (map[string]any, error) {
		raw, err := c.Raw(ctx, http.MethodGet, "/notification-providers/"+client.PathSegment(id), nil, nil)
		if client.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		obj := asObject(raw)
		if t, _ := obj["provider_type"].(string); t != kind {
			return nil, fmt.Errorf("notification provider %s has type %q; temps_%s manages %s providers only", id, t, name, kind)
		}
		return obj, nil
	}
}

var slackNotificationProviderSpec = notificationProviderSpec("slack",
	"A Slack notification provider: Temps posts alerts (failed deployments, backups, monitors) to a Slack incoming webhook.",
	[]gfield{
		{Name: "webhook_url", Kind: kString, Required: true, Sensitive: true, Desc: "Slack incoming webhook URL."},
		{Name: "channel", Kind: kString, Clearable: true, Desc: "Channel to post to instead of the webhook's default, for example `#alerts`."},
	})

var emailNotificationProviderSpec = notificationProviderSpec("email",
	"An email notification provider: Temps sends alerts through an SMTP server. For transactional email "+
		"sent by apps, see `temps_email_provider`.",
	[]gfield{
		{Name: "smtp_host", Kind: kString, Required: true, Desc: "SMTP server host name."},
		{Name: "smtp_port", Kind: kInt, Required: true, Desc: "SMTP server port, for example `587`."},
		{Name: "username", Kind: kString, Required: true, Desc: "SMTP user name."},
		{Name: "password", Kind: kString, Required: true, Sensitive: true, Desc: "SMTP password."},
		{Name: "from_address", Kind: kString, Required: true, Desc: "Sender address."},
		{Name: "from_name", Kind: kString, Clearable: true, Desc: "Sender name."},
		{Name: "to_addresses", Kind: kStringList, Required: true, Desc: "Recipients."},
		{Name: "tls_mode", Kind: kString, Optional: true, Default: "Starttls", OneOf: []string{"None", "Starttls", "Tls"},
			Desc: "How the connection is encrypted: `Starttls` (default) upgrades a plain connection, `Tls` connects over TLS, `None` does not encrypt."},
		{Name: "starttls_required", Kind: kBool, Optional: true, Default: true,
			Desc: "With `Starttls`, refuse a server that does not offer it."},
		{Name: "accept_invalid_certs", Kind: kBool, Optional: true, Default: false,
			Desc: "Accept a self-signed or otherwise invalid server certificate."},
	})

var telegramNotificationProviderSpec = notificationProviderSpec("telegram",
	"A Telegram notification provider: a bot posts alerts to a chat, group or channel.",
	[]gfield{
		{Name: "bot_token", Kind: kString, Required: true, Sensitive: true, Desc: "Bot token from @BotFather, `123456789:AA...`."},
		{Name: "chat_id", Kind: kString, Required: true,
			Desc: "Chat to post to: a numeric user, group or channel ID (`-100...` for supergroups and channels) or a public `@channelusername`."},
		{Name: "message_thread_id", Kind: kInt, Clearable: true, Desc: "Forum topic to post in, for groups with topics."},
	})

var cloudflareNotificationProviderSpec = notificationProviderSpec("cloudflare",
	"A Cloudflare Email Sending notification provider: Temps sends alerts through Cloudflare's transactional email API.",
	[]gfield{
		{Name: "account_id", Kind: kString, Required: true, Desc: "Cloudflare account ID that owns the Email Sending configuration."},
		{Name: "api_token", Kind: kString, Required: true, Sensitive: true, Desc: "Cloudflare API token with the Email Sending permission."},
		{Name: "from_address", Kind: kString, Required: true, Desc: "Verified sender address, on a domain enabled for Email Sending."},
		{Name: "from_name", Kind: kString, Clearable: true, Desc: "Sender name."},
		{Name: "to_addresses", Kind: kStringList, Required: true, Desc: "Recipients."},
	})
