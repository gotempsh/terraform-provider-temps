// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"net/http"
	"net/url"
)

// PresetConfig is the build configuration attached to a project's preset.
// The server replaces it wholesale on update, so callers always send every
// field they want to keep.
type PresetConfig struct {
	Preset         string  `json:"preset,omitempty"`
	BuildCommand   *string `json:"buildCommand,omitempty"`
	InstallCommand *string `json:"installCommand,omitempty"`
	OutputDir      *string `json:"outputDir,omitempty"`
	BuildContext   *string `json:"buildContext,omitempty"`
	DockerfilePath *string `json:"dockerfilePath,omitempty"`
}

// DeploymentConfig is the subset of a project's deployment configuration the
// provider manages.
type DeploymentConfig struct {
	ExposedPort     *int64 `json:"exposedPort,omitempty"`
	AutomaticDeploy *bool  `json:"automaticDeploy,omitempty"`
}

// Project is a Temps project.
type Project struct {
	ID                        int64            `json:"id"`
	Slug                      string           `json:"slug"`
	Name                      string           `json:"name"`
	SourceType                string           `json:"source_type"`
	GitURL                    *string          `json:"git_url"`
	RepoOwner                 *string          `json:"repo_owner"`
	RepoName                  *string          `json:"repo_name"`
	GitProviderConnectionID   *int64           `json:"git_provider_connection_id"`
	IsPublicRepo              bool             `json:"is_public_repo"`
	MainBranch                string           `json:"main_branch"`
	Directory                 string           `json:"directory"`
	Preset                    *string          `json:"preset"`
	PresetConfig              *PresetConfig    `json:"preset_config"`
	ProjectType               string           `json:"project_type"`
	EnablePreviewEnvironments bool             `json:"enable_preview_environments"`
	DeploymentConfig          DeploymentConfig `json:"deployment_config"`
}

// CreateProjectRequest is the body of POST /projects.
type CreateProjectRequest struct {
	Name                    string        `json:"name"`
	ExpectedSlug            *string       `json:"expected_slug,omitempty"`
	SourceType              *string       `json:"source_type,omitempty"`
	GitURL                  *string       `json:"git_url,omitempty"`
	RepoOwner               *string       `json:"repo_owner,omitempty"`
	RepoName                *string       `json:"repo_name,omitempty"`
	GitProviderConnectionID *int64        `json:"git_provider_connection_id,omitempty"`
	IsPublicRepo            *bool         `json:"is_public_repo,omitempty"`
	MainBranch              *string       `json:"main_branch,omitempty"`
	Directory               *string       `json:"directory,omitempty"`
	Preset                  *string       `json:"preset,omitempty"`
	PresetConfig            *PresetConfig `json:"preset_config,omitempty"`
	ExposedPort             *int64        `json:"exposed_port,omitempty"`
	AutomaticDeploy         *bool         `json:"automatic_deploy,omitempty"`
}

// UpdateProjectSettingsRequest is the body of POST /projects/{id}/settings.
// Every field is optional; omitted fields are left unchanged.
type UpdateProjectSettingsRequest struct {
	Name                      *string       `json:"name,omitempty"`
	Slug                      *string       `json:"slug,omitempty"`
	RepoOwner                 *string       `json:"repo_owner,omitempty"`
	RepoName                  *string       `json:"repo_name,omitempty"`
	GitProviderConnectionID   *int64        `json:"git_provider_connection_id,omitempty"`
	MainBranch                *string       `json:"main_branch,omitempty"`
	Directory                 *string       `json:"directory,omitempty"`
	Preset                    *string       `json:"preset,omitempty"`
	PresetConfig              *PresetConfig `json:"preset_config,omitempty"`
	EnablePreviewEnvironments *bool         `json:"enable_preview_environments,omitempty"`
}

type projectList struct {
	Projects []Project `json:"projects"`
}

// CreateProject creates a project.
func (c *Client) CreateProject(ctx context.Context, req CreateProjectRequest) (*Project, error) {
	var p Project
	return &p, c.do(ctx, http.MethodPost, "/projects", nil, req, &p)
}

// GetProject reads a project by ID.
func (c *Client) GetProject(ctx context.Context, id int64) (*Project, error) {
	var p Project
	return &p, c.do(ctx, http.MethodGet, idPath("/projects/%d", id), nil, nil, &p)
}

// GetProjectBySlug reads a project by slug.
func (c *Client) GetProjectBySlug(ctx context.Context, slug string) (*Project, error) {
	var p Project
	return &p, c.do(ctx, http.MethodGet, "/projects/by-slug/"+url.PathEscape(slug), nil, nil, &p)
}

// UpdateProjectSettings partially updates a project.
func (c *Client) UpdateProjectSettings(ctx context.Context, id int64, req UpdateProjectSettingsRequest) (*Project, error) {
	var p Project
	return &p, c.do(ctx, http.MethodPost, idPath("/projects/%d/settings", id), nil, req, &p)
}

// ChangeProjectSource switches a project's source type.
func (c *Client) ChangeProjectSource(ctx context.Context, id int64, sourceType string) (*Project, error) {
	var p Project
	body := map[string]string{"source_type": sourceType}
	return &p, c.do(ctx, http.MethodPatch, idPath("/projects/%d/source", id), nil, body, &p)
}

// SetProjectAutomaticDeploy toggles automatic deploys on push.
func (c *Client) SetProjectAutomaticDeploy(ctx context.Context, id int64, enabled bool) (*Project, error) {
	var p Project
	body := map[string]bool{"automatic_deploy": enabled}
	return &p, c.do(ctx, http.MethodPost, idPath("/projects/%d/automatic-deploy", id), nil, body, &p)
}

// SetProjectExposedPort sets the container port the project's app listens on.
func (c *Client) SetProjectExposedPort(ctx context.Context, id int64, port int64) (*Project, error) {
	var p Project
	body := map[string]int64{"exposedPort": port}
	return &p, c.do(ctx, http.MethodPatch, idPath("/projects/%d/deployment-config", id), nil, body, &p)
}

// DeleteProject deletes a project. A 404 is reported to the caller.
func (c *Client) DeleteProject(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/projects/%d", id), nil, nil, nil)
}

// ListProjects returns one page of projects.
func (c *Client) ListProjects(ctx context.Context, page, perPage int) ([]Project, error) {
	var out projectList
	q := url.Values{}
	q.Set("page", itoa(int64(page)))
	q.Set("per_page", itoa(int64(perPage)))
	return out.Projects, c.do(ctx, http.MethodGet, "/projects", q, nil, &out)
}
