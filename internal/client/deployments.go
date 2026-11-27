// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"net/http"
)

// DeployImageRequest is the body of
// POST /projects/{id}/environments/{env}/deploy/image.
type DeployImageRequest struct {
	ImageRef string `json:"image_ref"`
	// HealthCheckPath is the path Temps probes once the container starts;
	// nil uses the project's saved path, or "/".
	HealthCheckPath *string `json:"health_check_path,omitempty"`
	// Command overrides the image's command, one argv element per entry;
	// nil uses the project's saved command, or the image's.
	Command []string `json:"command,omitempty"`
}

// DeploymentStarted is the 202 answer to a deploy request.
type DeploymentStarted struct {
	ID            int64     `json:"id"`
	ProjectID     int64     `json:"project_id"`
	EnvironmentID int64     `json:"environment_id"`
	Slug          string    `json:"slug"`
	State         string    `json:"state"`
	CreatedAt     Timestamp `json:"created_at"`
}

// DeploymentFailure is Temps' classified cause of a failed deployment.
type DeploymentFailure struct {
	Stage       string  `json:"stage"`
	Code        string  `json:"code"`
	Title       string  `json:"title"`
	Remediation string  `json:"remediation"`
	FailedJob   *string `json:"failed_job"`
}

// Deployment is GET /projects/{id}/deployments/{deployment} (the fields the
// provider uses).
type Deployment struct {
	ID              int64              `json:"id"`
	ProjectID       int64              `json:"project_id"`
	EnvironmentID   int64              `json:"environment_id"`
	Status          string             `json:"status"`
	URL             string             `json:"url"`
	IsCurrent       bool               `json:"is_current"`
	CreatedAt       Timestamp          `json:"created_at"`
	FinishedAt      *Timestamp         `json:"finished_at"`
	CancelledReason *string            `json:"cancelled_reason"`
	Failure         *DeploymentFailure `json:"failure"`
}

// DeployImage starts a deployment of a registry image to an environment.
// Temps answers once the deployment is queued; poll GetDeployment for the
// outcome. POST is not retried after a gateway error, so a deploy is never
// started twice by the client.
func (c *Client) DeployImage(ctx context.Context, projectID, environmentID int64, req DeployImageRequest) (*DeploymentStarted, error) {
	var out DeploymentStarted
	return &out, c.do(ctx, http.MethodPost, idPath("/projects/%d/environments/%d/deploy/image", projectID, environmentID), nil, req, &out)
}

// GetDeployment reads a deployment of a project.
func (c *Client) GetDeployment(ctx context.Context, projectID, deploymentID int64) (*Deployment, error) {
	var out Deployment
	return &out, c.do(ctx, http.MethodGet, idPath("/projects/%d/deployments/%d", projectID, deploymentID), nil, nil, &out)
}

// CancelDeployment stops a deployment that has not finished. Temps answers
// 400 when it already completed, failed or was cancelled.
func (c *Client) CancelDeployment(ctx context.Context, projectID, deploymentID int64) error {
	return c.do(ctx, http.MethodPost, idPath("/projects/%d/deployments/%d/cancel", projectID, deploymentID), nil, nil, nil)
}
