// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"net/http"
)

// CronSchedule is when a cron job is expected to check in: a crontab
// expression, or a fixed interval.
type CronSchedule struct {
	Type  string `json:"type"`
	Value any    `json:"value"` // crontab: string; interval: integer
	Unit  string `json:"unit,omitempty"`
}

// CronMonitorConfig is a cron monitor's schedule and tolerances.
type CronMonitorConfig struct {
	Schedule              CronSchedule `json:"schedule"`
	Timezone              string       `json:"timezone,omitempty"`
	CheckinMargin         *int64       `json:"checkin_margin,omitempty"`
	MaxRuntime            *int64       `json:"max_runtime,omitempty"`
	FailureIssueThreshold *int64       `json:"failure_issue_threshold,omitempty"`
	RecoveryThreshold     *int64       `json:"recovery_threshold,omitempty"`
}

// CronMonitor watches that a scheduled job checks in on time.
type CronMonitor struct {
	ID                  int64              `json:"id"`
	ProjectID           int64              `json:"project_id"`
	EnvironmentID       *int64             `json:"environment_id"`
	Slug                string             `json:"slug"`
	Config              *CronMonitorConfig `json:"config"`
	Enabled             bool               `json:"enabled"`
	ConfigurationSource string             `json:"configuration_source"`
	Status              string             `json:"status"`
}

// CreateCronMonitorRequest is the body of POST /projects/{id}/cron-monitors.
type CreateCronMonitorRequest struct {
	Slug          string            `json:"slug"`
	EnvironmentID *int64            `json:"environment_id,omitempty"`
	Config        CronMonitorConfig `json:"config"`
}

// UpdateCronMonitorRequest is the body of PATCH .../cron-monitors/{id}. The
// server replaces the whole config, resetting omitted tolerances to their
// defaults, so callers send every field.
type UpdateCronMonitorRequest struct {
	Config  *CronMonitorConfig `json:"config,omitempty"`
	Enabled *bool              `json:"enabled,omitempty"`
}

// CreateCronMonitor creates a cron monitor.
func (c *Client) CreateCronMonitor(ctx context.Context, projectID int64, req CreateCronMonitorRequest) (*CronMonitor, error) {
	var m CronMonitor
	return &m, c.do(ctx, http.MethodPost, idPath("/projects/%d/cron-monitors", projectID), nil, req, &m)
}

// GetCronMonitor reads a cron monitor.
func (c *Client) GetCronMonitor(ctx context.Context, projectID, id int64) (*CronMonitor, error) {
	var m CronMonitor
	return &m, c.do(ctx, http.MethodGet, idPath("/projects/%d/cron-monitors/%d", projectID, id), nil, nil, &m)
}

// UpdateCronMonitor updates a cron monitor.
func (c *Client) UpdateCronMonitor(ctx context.Context, projectID, id int64, req UpdateCronMonitorRequest) (*CronMonitor, error) {
	var m CronMonitor
	return &m, c.do(ctx, http.MethodPatch, idPath("/projects/%d/cron-monitors/%d", projectID, id), nil, req, &m)
}

// DeleteCronMonitor deletes a cron monitor.
func (c *Client) DeleteCronMonitor(ctx context.Context, projectID, id int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/projects/%d/cron-monitors/%d", projectID, id), nil, nil, nil)
}
