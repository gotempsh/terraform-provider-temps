// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// Environment is a deployment environment of a project.
type Environment struct {
	ID         int64   `json:"id"`
	ProjectID  int64   `json:"project_id"`
	Name       string  `json:"name"`
	Slug       string  `json:"slug"`
	Branch     *string `json:"branch"`
	MainURL    string  `json:"main_url"`
	Subdomain  string  `json:"subdomain"`
	IsPreview  bool    `json:"is_preview"`
	Protected  bool    `json:"protected"`
	ForceHTTPS *bool   `json:"force_https"`
}

// CreateEnvironmentRequest is the body of POST /projects/{id}/environments.
type CreateEnvironmentRequest struct {
	Name         string `json:"name"`
	Branch       string `json:"branch"`
	SetAsPreview bool   `json:"set_as_preview,omitempty"`
}

// UpdateEnvironmentSettingsRequest is the subset of environment settings the
// provider manages. Omitted fields are unchanged.
type UpdateEnvironmentSettingsRequest struct {
	Branch     *string `json:"branch,omitempty"`
	Protected  *bool   `json:"protected,omitempty"`
	ForceHTTPS *bool   `json:"force_https,omitempty"`
}

// CreateEnvironment creates an environment.
func (c *Client) CreateEnvironment(ctx context.Context, projectID int64, req CreateEnvironmentRequest) (*Environment, error) {
	var e Environment
	return &e, c.do(ctx, http.MethodPost, idPath("/projects/%d/environments", projectID), nil, req, &e)
}

// GetEnvironment reads an environment.
func (c *Client) GetEnvironment(ctx context.Context, projectID, envID int64) (*Environment, error) {
	var e Environment
	return &e, c.do(ctx, http.MethodGet, idPath("/projects/%d/environments/%d", projectID, envID), nil, nil, &e)
}

// ListEnvironments lists a project's environments.
func (c *Client) ListEnvironments(ctx context.Context, projectID int64) ([]Environment, error) {
	var out []Environment
	return out, c.do(ctx, http.MethodGet, idPath("/projects/%d/environments", projectID), nil, nil, &out)
}

// UpdateEnvironmentSettings updates an environment.
func (c *Client) UpdateEnvironmentSettings(ctx context.Context, projectID, envID int64, req UpdateEnvironmentSettingsRequest) (*Environment, error) {
	var e Environment
	return &e, c.do(ctx, http.MethodPut, idPath("/projects/%d/environments/%d/settings", projectID, envID), nil, req, &e)
}

// DeleteEnvironment deletes an environment.
func (c *Client) DeleteEnvironment(ctx context.Context, projectID, envID int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/projects/%d/environments/%d", projectID, envID), nil, nil, nil)
}

// EnvironmentRef is an environment an environment variable applies to.
type EnvironmentRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// EnvironmentVariable is a project environment variable. List responses mask
// Value ("***" for plain values, null for secrets); use
// GetEnvironmentVariableValue to read a plain value.
type EnvironmentVariable struct {
	ID               int64            `json:"id"`
	Key              string           `json:"key"`
	Value            *string          `json:"value"`
	IsSecret         bool             `json:"is_secret"`
	IncludeInPreview bool             `json:"include_in_preview"`
	Environments     []EnvironmentRef `json:"environments"`
}

// EnvironmentVariableRequest is the body for creating or updating a variable.
type EnvironmentVariableRequest struct {
	Key              string  `json:"key"`
	Value            *string `json:"value,omitempty"`
	EnvironmentIDs   []int64 `json:"environment_ids"`
	IsSecret         *bool   `json:"is_secret,omitempty"`
	IncludeInPreview bool    `json:"include_in_preview"`
}

// CreateEnvironmentVariable creates a variable.
func (c *Client) CreateEnvironmentVariable(ctx context.Context, projectID int64, req EnvironmentVariableRequest) (*EnvironmentVariable, error) {
	var v EnvironmentVariable
	return &v, c.do(ctx, http.MethodPost, idPath("/projects/%d/env-vars", projectID), nil, req, &v)
}

// UpdateEnvironmentVariable updates a variable. A nil Value keeps the stored value.
func (c *Client) UpdateEnvironmentVariable(ctx context.Context, projectID, varID int64, req EnvironmentVariableRequest) (*EnvironmentVariable, error) {
	var v EnvironmentVariable
	return &v, c.do(ctx, http.MethodPut, idPath("/projects/%d/env-vars/%d", projectID, varID), nil, req, &v)
}

// ListEnvironmentVariables lists a project's variables (values masked).
func (c *Client) ListEnvironmentVariables(ctx context.Context, projectID int64) ([]EnvironmentVariable, error) {
	var out []EnvironmentVariable
	return out, c.do(ctx, http.MethodGet, idPath("/projects/%d/env-vars", projectID), nil, nil, &out)
}

// GetEnvironmentVariable finds one variable by ID. There is no single-item
// endpoint, so it lists and filters; a missing ID is reported as a 404 APIError.
func (c *Client) GetEnvironmentVariable(ctx context.Context, projectID, varID int64) (*EnvironmentVariable, error) {
	vars, err := c.ListEnvironmentVariables(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for i := range vars {
		if vars[i].ID == varID {
			return &vars[i], nil
		}
	}
	return nil, &APIError{
		StatusCode: http.StatusNotFound, Method: http.MethodGet,
		Path:   idPath("/projects/%d/env-vars", projectID),
		Title:  "Not Found",
		Detail: "environment variable " + itoa(varID) + " not found in project " + itoa(projectID),
	}
}

// GetEnvironmentVariableValue reveals a non-secret variable's value. Secret
// values are write-only and return a 403.
func (c *Client) GetEnvironmentVariableValue(ctx context.Context, projectID, varID int64, key string) (string, error) {
	var out struct {
		Value string `json:"value"`
	}
	q := url.Values{}
	q.Set("var_id", itoa(varID))
	err := c.do(ctx, http.MethodGet, idPath("/projects/%d/env-vars/", projectID)+url.PathEscape(key)+"/value", q, nil, &out)
	return out.Value, err
}

// DeleteEnvironmentVariable deletes a variable.
func (c *Client) DeleteEnvironmentVariable(ctx context.Context, projectID, varID int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/projects/%d/env-vars/%d", projectID, varID), nil, nil, nil)
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
