// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// FeatureFlag is a project feature flag. Values are raw JSON so bool,
// string, number and JSON flags share one shape.
type FeatureFlag struct {
	ID            int64                    `json:"id"`
	Key           string                   `json:"key"`
	ValueType     string                   `json:"value_type"`
	DefaultValue  json.RawMessage          `json:"default_value"`
	Description   *string                  `json:"description"`
	ClientVisible bool                     `json:"client_visible"`
	ArchivedAt    *string                  `json:"archived_at"`
	Environments  []FeatureFlagEnvironment `json:"environments"`
}

// FeatureFlagEnvironment is a flag's override and kill switch in one environment.
type FeatureFlagEnvironment struct {
	EnvironmentID int64           `json:"environment_id"`
	Enabled       bool            `json:"enabled"`
	Value         json.RawMessage `json:"value"`
}

// HasValue reports whether the environment overrides the flag default.
func (e FeatureFlagEnvironment) HasValue() bool {
	return len(e.Value) > 0 && string(e.Value) != "null"
}

// CreateFeatureFlagRequest is the body of POST /projects/{id}/flags.
type CreateFeatureFlagRequest struct {
	Key           string          `json:"key"`
	ValueType     string          `json:"value_type"`
	DefaultValue  json.RawMessage `json:"default_value"`
	Description   *string         `json:"description,omitempty"`
	ClientVisible bool            `json:"client_visible"`
}

// UpdateFeatureFlagRequest is the body of PATCH /projects/{id}/flags/{key}.
// Description is tri-state on the server: a JSON null clears it, so it is
// always sent.
type UpdateFeatureFlagRequest struct {
	DefaultValue  json.RawMessage `json:"default_value,omitempty"`
	Description   *string         `json:"description"`
	ClientVisible *bool           `json:"client_visible,omitempty"`
}

func flagPath(projectID int64, key string) string {
	return idPath("/projects/%d/flags/", projectID) + url.PathEscape(key)
}

// CreateFeatureFlag creates a feature flag.
func (c *Client) CreateFeatureFlag(ctx context.Context, projectID int64, req CreateFeatureFlagRequest) (*FeatureFlag, error) {
	var f FeatureFlag
	return &f, c.do(ctx, http.MethodPost, idPath("/projects/%d/flags", projectID), nil, req, &f)
}

// GetFeatureFlag reads a feature flag, archived or not.
func (c *Client) GetFeatureFlag(ctx context.Context, projectID int64, key string) (*FeatureFlag, error) {
	var f FeatureFlag
	return &f, c.do(ctx, http.MethodGet, flagPath(projectID, key), nil, nil, &f)
}

// UpdateFeatureFlag partially updates a feature flag.
func (c *Client) UpdateFeatureFlag(ctx context.Context, projectID int64, key string, req UpdateFeatureFlagRequest) (*FeatureFlag, error) {
	var f FeatureFlag
	return &f, c.do(ctx, http.MethodPatch, flagPath(projectID, key), nil, req, &f)
}

// ArchiveFeatureFlag archives a feature flag. Temps has no hard delete: the
// key stays reserved until the flag is restored.
func (c *Client) ArchiveFeatureFlag(ctx context.Context, projectID int64, key string) error {
	return c.do(ctx, http.MethodDelete, flagPath(projectID, key), nil, nil, nil)
}

// RestoreFeatureFlag un-archives a feature flag.
func (c *Client) RestoreFeatureFlag(ctx context.Context, projectID int64, key string) (*FeatureFlag, error) {
	var f FeatureFlag
	return &f, c.do(ctx, http.MethodPost, flagPath(projectID, key)+"/restore", nil, nil, &f)
}

// SetFeatureFlagEnvironmentRequest sets a flag's state in one environment.
// Value is tri-state on the server: a JSON null clears the override, so it is
// always sent.
type SetFeatureFlagEnvironmentRequest struct {
	Enabled bool            `json:"enabled"`
	Value   json.RawMessage `json:"value"`
}

// SetFeatureFlagEnvironment sets a flag's override and kill switch in one environment.
func (c *Client) SetFeatureFlagEnvironment(ctx context.Context, projectID int64, key string, envID int64, req SetFeatureFlagEnvironmentRequest) (*FeatureFlagEnvironment, error) {
	if req.Value == nil {
		req.Value = json.RawMessage("null")
	}
	var e FeatureFlagEnvironment
	return &e, c.do(ctx, http.MethodPut, flagPath(projectID, key)+idPath("/environments/%d", envID), nil, req, &e)
}
