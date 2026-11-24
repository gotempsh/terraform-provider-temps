// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"net/http"
)

// CustomDomain routes a hostname to a project environment, or redirects it.
type CustomDomain struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	Domain      string `json:"domain"`
	Environment *struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"environment"`
	RedirectTo  *string `json:"redirect_to"`
	StatusCode  *int64  `json:"status_code"`
	Branch      *string `json:"branch"`
	ServiceName *string `json:"service_name"`
	Status      string  `json:"status"`
	Message     *string `json:"message"`
}

// EnvironmentID returns the environment the domain routes to, or 0.
func (d *CustomDomain) EnvironmentID() int64 {
	if d.Environment == nil {
		return 0
	}
	return d.Environment.ID
}

// CustomDomainRequest creates a custom domain, or updates one: on update nil
// fields are unchanged and an empty RedirectTo, Branch or ServiceName clears it.
type CustomDomainRequest struct {
	Domain        *string `json:"domain,omitempty"`
	EnvironmentID *int64  `json:"environment_id,omitempty"`
	RedirectTo    *string `json:"redirect_to,omitempty"`
	StatusCode    *int64  `json:"status_code,omitempty"`
	Branch        *string `json:"branch,omitempty"`
	ServiceName   *string `json:"service_name,omitempty"`
}

// CreateCustomDomain adds a custom domain to a project.
func (c *Client) CreateCustomDomain(ctx context.Context, projectID int64, req CustomDomainRequest) (*CustomDomain, error) {
	var d CustomDomain
	return &d, c.do(ctx, http.MethodPost, idPath("/projects/%d/custom-domains", projectID), nil, req, &d)
}

// GetCustomDomain reads a custom domain.
func (c *Client) GetCustomDomain(ctx context.Context, projectID, id int64) (*CustomDomain, error) {
	var d CustomDomain
	return &d, c.do(ctx, http.MethodGet, idPath("/projects/%d/custom-domains/%d", projectID, id), nil, nil, &d)
}

// UpdateCustomDomain partially updates a custom domain.
func (c *Client) UpdateCustomDomain(ctx context.Context, projectID, id int64, req CustomDomainRequest) (*CustomDomain, error) {
	var d CustomDomain
	return &d, c.do(ctx, http.MethodPut, idPath("/projects/%d/custom-domains/%d", projectID, id), nil, req, &d)
}

// DeleteCustomDomain removes a custom domain.
func (c *Client) DeleteCustomDomain(ctx context.Context, projectID, id int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/projects/%d/custom-domains/%d", projectID, id), nil, nil, nil)
}

// Webhook delivers project events to a URL.
type Webhook struct {
	ID        int64    `json:"id"`
	ProjectID int64    `json:"project_id"`
	URL       string   `json:"url"`
	Events    []string `json:"events"`
	Enabled   bool     `json:"enabled"`
	HasSecret bool     `json:"has_secret"`
}

// WebhookRequest creates a webhook, or updates one (nil fields unchanged).
type WebhookRequest struct {
	URL     *string   `json:"url,omitempty"`
	Events  *[]string `json:"events,omitempty"`
	Enabled *bool     `json:"enabled,omitempty"`
	Secret  *string   `json:"secret,omitempty"`
}

// CreateWebhook creates a project webhook.
func (c *Client) CreateWebhook(ctx context.Context, projectID int64, req WebhookRequest) (*Webhook, error) {
	var w Webhook
	return &w, c.do(ctx, http.MethodPost, idPath("/projects/%d/webhooks", projectID), nil, req, &w)
}

// GetWebhook reads a project webhook.
func (c *Client) GetWebhook(ctx context.Context, projectID, id int64) (*Webhook, error) {
	var w Webhook
	return &w, c.do(ctx, http.MethodGet, idPath("/projects/%d/webhooks/%d", projectID, id), nil, nil, &w)
}

// UpdateWebhook partially updates a project webhook.
func (c *Client) UpdateWebhook(ctx context.Context, projectID, id int64, req WebhookRequest) (*Webhook, error) {
	var w Webhook
	return &w, c.do(ctx, http.MethodPut, idPath("/projects/%d/webhooks/%d", projectID, id), nil, req, &w)
}

// DeleteWebhook deletes a project webhook.
func (c *Client) DeleteWebhook(ctx context.Context, projectID, id int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/projects/%d/webhooks/%d", projectID, id), nil, nil, nil)
}

// WebhookEventType is an event a webhook can subscribe to.
type WebhookEventType struct {
	EventType   string `json:"event_type"`
	Description string `json:"description"`
	Category    string `json:"category"`
}

// ListWebhookEventTypes lists the events webhooks can subscribe to.
func (c *Client) ListWebhookEventTypes(ctx context.Context) ([]WebhookEventType, error) {
	var out []WebhookEventType
	return out, c.do(ctx, http.MethodGet, "/webhook-event-types", nil, nil, &out)
}
