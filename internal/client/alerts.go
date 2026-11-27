// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ProjectSecret is an encrypted project secret. Its value is never returned.
type ProjectSecret struct {
	ID               int64    `json:"id"`
	ProjectID        int64    `json:"project_id"`
	Key              string   `json:"key"`
	IncludeInPreview bool     `json:"include_in_preview"`
	ComposeServices  []string `json:"compose_services"`
	Environments     []struct {
		ID int64 `json:"id"`
	} `json:"environments"`
}

// EnvironmentIDs returns the IDs of the environments the secret applies to;
// none means every environment of the project.
func (s *ProjectSecret) EnvironmentIDs() []int64 {
	ids := make([]int64, 0, len(s.Environments))
	for _, e := range s.Environments {
		ids = append(ids, e.ID)
	}
	return ids
}

// ProjectSecretRequest creates or updates a project secret. On update Temps
// treats an omitted environment_ids, include_in_preview or compose_services
// as empty or false, so every update must send all three.
type ProjectSecretRequest struct {
	Key              string   `json:"key,omitempty"`
	Value            *string  `json:"value,omitempty"`
	EnvironmentIDs   []int64  `json:"environment_ids"`
	IncludeInPreview bool     `json:"include_in_preview"`
	ComposeServices  []string `json:"compose_services"`
}

// CreateProjectSecret creates a project secret.
func (c *Client) CreateProjectSecret(ctx context.Context, projectID int64, req ProjectSecretRequest) (*ProjectSecret, error) {
	var s ProjectSecret
	return &s, c.do(ctx, http.MethodPost, idPath("/projects/%d/secrets", projectID), nil, req, &s)
}

// GetProjectSecret finds a secret in the project's list, reading every page.
// Servers from 2026-11-25 on page the list and also have a single-secret
// GET; earlier releases send the whole list as an array and have none, so
// the list is read on every version.
func (c *Client) GetProjectSecret(ctx context.Context, projectID, id int64) (*ProjectSecret, error) {
	// A missing project may list as empty rather than 404, which reads the
	// same as a deleted secret: both remove it from state.
	p := idPath("/projects/%d/secrets", projectID)
	s, err := findInList(ctx, c, p, nil, func(s *ProjectSecret) bool { return s.ID == id })
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, notFoundError(p, fmt.Sprintf("Secret %d not found in project %d", id, projectID))
	}
	return s, nil
}

// UpdateProjectSecret replaces a secret's scope, and its value when given.
func (c *Client) UpdateProjectSecret(ctx context.Context, projectID, id int64, req ProjectSecretRequest) (*ProjectSecret, error) {
	req.Key = ""
	var s ProjectSecret
	return &s, c.do(ctx, http.MethodPut, idPath("/projects/%d/secrets/%d", projectID, id), nil, req, &s)
}

// DeleteProjectSecret deletes a project secret.
func (c *Client) DeleteProjectSecret(ctx context.Context, projectID, id int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/projects/%d/secrets/%d", projectID, id), nil, nil, nil)
}

// NotificationRoute sends alerts in a severity range to notification providers.
type NotificationRoute struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Enabled     bool    `json:"enabled"`
	MinSeverity string  `json:"min_severity"`
	MaxSeverity string  `json:"max_severity"`
	ProviderIDs []int64 `json:"provider_ids"`
}

// NotificationRouteRequest creates or replaces a notification route.
type NotificationRouteRequest struct {
	Name        string  `json:"name"`
	Enabled     bool    `json:"enabled"`
	MinSeverity string  `json:"min_severity"`
	MaxSeverity string  `json:"max_severity"`
	ProviderIDs []int64 `json:"provider_ids"`
}

// CreateNotificationRoute creates a notification route.
func (c *Client) CreateNotificationRoute(ctx context.Context, req NotificationRouteRequest) (*NotificationRoute, error) {
	var r NotificationRoute
	return &r, c.do(ctx, http.MethodPost, "/notification-routes", nil, req, &r)
}

// GetNotificationRoute reads a notification route.
func (c *Client) GetNotificationRoute(ctx context.Context, id int64) (*NotificationRoute, error) {
	var r NotificationRoute
	return &r, c.do(ctx, http.MethodGet, idPath("/notification-routes/%d", id), nil, nil, &r)
}

// UpdateNotificationRoute replaces a notification route's settings.
func (c *Client) UpdateNotificationRoute(ctx context.Context, id int64, req NotificationRouteRequest) (*NotificationRoute, error) {
	var r NotificationRoute
	return &r, c.do(ctx, http.MethodPut, idPath("/notification-routes/%d", id), nil, req, &r)
}

// DeleteNotificationRoute deletes a notification route.
func (c *Client) DeleteNotificationRoute(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/notification-routes/%d", id), nil, nil, nil)
}

// ErrorAlertRule notifies when a project's error tracking sees a trigger.
type ErrorAlertRule struct {
	ID                   int64           `json:"id"`
	ProjectID            int64           `json:"project_id"`
	Name                 string          `json:"name"`
	TriggerType          string          `json:"trigger_type"`
	TriggerConfig        json.RawMessage `json:"trigger_config"`
	EnvironmentFilter    *int64          `json:"environment_filter"`
	ErrorLevelFilter     *string         `json:"error_level_filter"`
	NotificationPriority string          `json:"notification_priority"`
	CooldownMinutes      int64           `json:"cooldown_minutes"`
	Enabled              bool            `json:"enabled"`
}

// ErrorAlertRuleRequest creates or updates an error alert rule. Temps ignores
// a null environment_filter or error_level_filter on update: neither can be
// removed in place.
type ErrorAlertRuleRequest struct {
	Name                 string          `json:"name"`
	TriggerType          string          `json:"trigger_type"`
	TriggerConfig        json.RawMessage `json:"trigger_config"`
	EnvironmentFilter    *int64          `json:"environment_filter,omitempty"`
	ErrorLevelFilter     *string         `json:"error_level_filter,omitempty"`
	NotificationPriority string          `json:"notification_priority"`
	CooldownMinutes      int64           `json:"cooldown_minutes"`
	Enabled              bool            `json:"enabled"`
}

// CreateErrorAlertRule creates an error alert rule.
func (c *Client) CreateErrorAlertRule(ctx context.Context, projectID int64, req ErrorAlertRuleRequest) (*ErrorAlertRule, error) {
	var r ErrorAlertRule
	return &r, c.do(ctx, http.MethodPost, idPath("/projects/%d/error-alert-rules", projectID), nil, req, &r)
}

// GetErrorAlertRule reads an error alert rule.
func (c *Client) GetErrorAlertRule(ctx context.Context, projectID, id int64) (*ErrorAlertRule, error) {
	var r ErrorAlertRule
	err := c.do(ctx, http.MethodGet, idPath("/projects/%d/error-alert-rules/%d", projectID, id), nil, nil, &r)
	return &r, asRuleNotFound(err)
}

// UpdateErrorAlertRule updates an error alert rule.
func (c *Client) UpdateErrorAlertRule(ctx context.Context, projectID, id int64, req ErrorAlertRuleRequest) (*ErrorAlertRule, error) {
	var r ErrorAlertRule
	err := c.do(ctx, http.MethodPut, idPath("/projects/%d/error-alert-rules/%d", projectID, id), nil, req, &r)
	return &r, asRuleNotFound(err)
}

// DeleteErrorAlertRule deletes an error alert rule.
func (c *Client) DeleteErrorAlertRule(ctx context.Context, projectID, id int64) error {
	return asRuleNotFound(c.do(ctx, http.MethodDelete, idPath("/projects/%d/error-alert-rules/%d", projectID, id), nil, nil, nil))
}

// asRuleNotFound turns the 400 Temps returns for a rule of another project
// ("Alert rule N not found in project M") into the 404 it means.
func asRuleNotFound(err error) error {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest &&
		strings.Contains(strings.ToLower(apiErr.Detail), "not found in project") {
		cp := *apiErr
		cp.StatusCode = http.StatusNotFound
		return &cp
	}
	return err
}

// EdgeRuleAction is what a matching edge rule does.
type EdgeRuleAction struct {
	Type   string  `json:"type"`
	Status *int64  `json:"status,omitempty"`
	Target *string `json:"target,omitempty"`
	Body   *string `json:"body,omitempty"`
}

// EdgeRule is a request filter the proxy evaluates for a project.
type EdgeRule struct {
	ID            int64           `json:"id"`
	ProjectID     int64           `json:"project_id"`
	EnvironmentID *int64          `json:"environment_id"`
	Position      int64           `json:"position"`
	Name          string          `json:"name"`
	Enabled       bool            `json:"enabled"`
	Mode          string          `json:"mode"`
	Expression    json.RawMessage `json:"expression"`
	Action        EdgeRuleAction  `json:"action"`
	Revision      int64           `json:"revision"`
	Summary       string          `json:"summary"`
}

// EdgeRuleRequest creates an edge rule, or with Revision updates one.
// environment_id is always sent: null applies the rule to every environment.
type EdgeRuleRequest struct {
	Name          string          `json:"name"`
	EnvironmentID *int64          `json:"environment_id"`
	Enabled       bool            `json:"enabled"`
	Mode          string          `json:"mode"`
	Expression    json.RawMessage `json:"expression"`
	Action        EdgeRuleAction  `json:"action"`
	Revision      *int64          `json:"revision,omitempty"`
}

// CreateEdgeRule creates an edge rule at the end of the project's list.
func (c *Client) CreateEdgeRule(ctx context.Context, projectID int64, req EdgeRuleRequest) (*EdgeRule, error) {
	var r EdgeRule
	return &r, c.do(ctx, http.MethodPost, idPath("/projects/%d/edge-rules", projectID), nil, req, &r)
}

// GetEdgeRule reads an edge rule.
func (c *Client) GetEdgeRule(ctx context.Context, projectID, id int64) (*EdgeRule, error) {
	var r EdgeRule
	return &r, c.do(ctx, http.MethodGet, idPath("/projects/%d/edge-rules/%d", projectID, id), nil, nil, &r)
}

// UpdateEdgeRule updates an edge rule. With a revision, a rule changed since
// that revision is refused with 409 instead of overwritten.
func (c *Client) UpdateEdgeRule(ctx context.Context, projectID, id int64, req EdgeRuleRequest) (*EdgeRule, error) {
	var r EdgeRule
	return &r, c.do(ctx, http.MethodPatch, idPath("/projects/%d/edge-rules/%d", projectID, id), nil, req, &r)
}

// DeleteEdgeRule deletes an edge rule.
func (c *Client) DeleteEdgeRule(ctx context.Context, projectID, id int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/projects/%d/edge-rules/%d", projectID, id), nil, nil, nil)
}

// InboundFilter is a rule that drops matching error events before they are
// stored.
type InboundFilter struct {
	ID           int64   `json:"id"`
	ProjectID    int64   `json:"project_id"`
	Kind         string  `json:"kind"`
	Pattern      *string `json:"pattern"`
	Enabled      bool    `json:"enabled"`
	DroppedCount int64   `json:"dropped_count"`
}

// InboundFilterRequest is the body of POST and PATCH
// /projects/{id}/error-inbound-filters. PATCH ignores Kind.
type InboundFilterRequest struct {
	Kind    string  `json:"kind,omitempty"`
	Pattern *string `json:"pattern,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
}

type inboundFilterList struct {
	Filters []InboundFilter `json:"filters"`
}

// CreateInboundFilter adds an inbound filter to a project.
func (c *Client) CreateInboundFilter(ctx context.Context, projectID int64, req InboundFilterRequest) (*InboundFilter, error) {
	var f InboundFilter
	return &f, c.do(ctx, http.MethodPost, idPath("/projects/%d/error-inbound-filters", projectID), nil, req, &f)
}

// GetInboundFilter finds one filter in the project's list; Temps has no
// single-filter GET. The list is bounded by the project's limit.
func (c *Client) GetInboundFilter(ctx context.Context, projectID, id int64) (*InboundFilter, error) {
	var list inboundFilterList
	p := idPath("/projects/%d/error-inbound-filters", projectID)
	if err := c.do(ctx, http.MethodGet, p, nil, nil, &list); err != nil {
		return nil, err
	}
	for i := range list.Filters {
		if list.Filters[i].ID == id {
			return &list.Filters[i], nil
		}
	}
	return nil, notFoundError(p, fmt.Sprintf("Inbound filter %d not found in project %d", id, projectID))
}

// UpdateInboundFilter changes a filter's pattern or switches it on or off.
func (c *Client) UpdateInboundFilter(ctx context.Context, projectID, id int64, req InboundFilterRequest) (*InboundFilter, error) {
	req.Kind = ""
	var f InboundFilter
	return &f, c.do(ctx, http.MethodPatch, fmt.Sprintf("/projects/%d/error-inbound-filters/%d", projectID, id), nil, req, &f)
}

// DeleteInboundFilter removes an inbound filter.
func (c *Client) DeleteInboundFilter(ctx context.Context, projectID, id int64) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/projects/%d/error-inbound-filters/%d", projectID, id), nil, nil, nil)
}
