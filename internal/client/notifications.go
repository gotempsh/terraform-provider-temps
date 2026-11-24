// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"net/http"
)

// MaskedSecret is the placeholder the API returns for, and accepts back as
// "unchanged", every stored notification provider credential.
const MaskedSecret = "***"

// WebhookNotificationConfig is a webhook notification provider's settings.
// Read back, url and every header value are MaskedSecret.
type WebhookNotificationConfig struct {
	URL         string            `json:"url"`
	Method      string            `json:"method,omitempty"`
	Headers     map[string]string `json:"headers"`
	TimeoutSecs int64             `json:"timeout_secs,omitempty"`
}

// NotificationProvider is a channel Temps sends alerts through.
type NotificationProvider struct {
	ID           int64                     `json:"id"`
	Name         string                    `json:"name"`
	ProviderType string                    `json:"provider_type"`
	Enabled      bool                      `json:"enabled"`
	Config       WebhookNotificationConfig `json:"config"`
}

// WebhookNotificationProviderRequest creates or fully replaces a webhook
// notification provider.
type WebhookNotificationProviderRequest struct {
	Name    string                    `json:"name"`
	Enabled bool                      `json:"enabled"`
	Config  WebhookNotificationConfig `json:"config"`
}

// CreateWebhookNotificationProvider creates a webhook notification provider.
func (c *Client) CreateWebhookNotificationProvider(ctx context.Context, req WebhookNotificationProviderRequest) (*NotificationProvider, error) {
	var p NotificationProvider
	return &p, c.do(ctx, http.MethodPost, "/notification-providers/webhook", nil, req, &p)
}

// GetNotificationProvider reads a notification provider of any type.
func (c *Client) GetNotificationProvider(ctx context.Context, id int64) (*NotificationProvider, error) {
	var p NotificationProvider
	return &p, c.do(ctx, http.MethodGet, idPath("/notification-providers/%d", id), nil, nil, &p)
}

// UpdateWebhookNotificationProvider replaces a webhook notification
// provider. MaskedSecret for the url or a header value keeps the stored one.
func (c *Client) UpdateWebhookNotificationProvider(ctx context.Context, id int64, req WebhookNotificationProviderRequest) (*NotificationProvider, error) {
	var p NotificationProvider
	return &p, c.do(ctx, http.MethodPut, idPath("/notification-providers/webhook/%d", id), nil, req, &p)
}

// DeleteNotificationProvider deletes a notification provider.
func (c *Client) DeleteNotificationProvider(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/notification-providers/%d", id), nil, nil, nil)
}
