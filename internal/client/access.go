// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"fmt"
	"net/http"
)

// IPAccessCIDR is one entry of an IP access policy.
type IPAccessCIDR struct {
	CIDR        string  `json:"cidr"`
	Description *string `json:"description,omitempty"`
}

// IPAccessPolicy is a project-wide or per-environment IP access policy.
type IPAccessPolicy struct {
	ProjectID     int64          `json:"project_id"`
	EnvironmentID *int64         `json:"environment_id"`
	Configured    bool           `json:"configured"`
	Mode          string         `json:"mode"`
	CIDRs         []IPAccessCIDR `json:"cidrs"`
	Warnings      []string       `json:"warnings"`
}

// SetIPAccessPolicyRequest replaces an IP access policy.
type SetIPAccessPolicyRequest struct {
	Mode               string         `json:"mode"`
	CIDRs              []IPAccessCIDR `json:"cidrs"`
	AcknowledgeLockout bool           `json:"acknowledge_lockout"`
}

func ipAccessPath(projectID, envID int64) string {
	if envID == 0 {
		return idPath("/projects/%d/ip-access", projectID)
	}
	return idPath("/projects/%d/environments/%d/ip-access", projectID, envID)
}

// GetIPAccessPolicy reads the project policy (envID 0) or an environment's.
func (c *Client) GetIPAccessPolicy(ctx context.Context, projectID, envID int64) (*IPAccessPolicy, error) {
	var p IPAccessPolicy
	return &p, c.do(ctx, http.MethodGet, ipAccessPath(projectID, envID), nil, nil, &p)
}

// SetIPAccessPolicy replaces the project policy (envID 0) or an environment's.
func (c *Client) SetIPAccessPolicy(ctx context.Context, projectID, envID int64, req SetIPAccessPolicyRequest) (*IPAccessPolicy, error) {
	if req.CIDRs == nil {
		req.CIDRs = []IPAccessCIDR{}
	}
	var p IPAccessPolicy
	return &p, c.do(ctx, http.MethodPut, ipAccessPath(projectID, envID), nil, req, &p)
}

// DeleteIPAccessPolicy removes a stored policy; an environment then inherits
// the project policy.
func (c *Client) DeleteIPAccessPolicy(ctx context.Context, projectID, envID int64) error {
	return c.do(ctx, http.MethodDelete, ipAccessPath(projectID, envID), nil, nil, nil)
}

// DeploymentToken is a project token used by deployed apps and CI.
type DeploymentToken struct {
	ID            int64     `json:"id"`
	ProjectID     int64     `json:"project_id"`
	EnvironmentID *int64    `json:"environment_id"`
	Name          string    `json:"name"`
	TokenPrefix   string    `json:"token_prefix"`
	Permissions   *[]string `json:"permissions"`
	IsActive      *bool     `json:"is_active"`
	ExpiresAt     *string   `json:"expires_at"`
	// Token is only returned when the token is created.
	Token string `json:"token,omitempty"`
}

// CreateDeploymentTokenRequest is the body of POST /projects/{id}/deployment-tokens.
type CreateDeploymentTokenRequest struct {
	Name          string    `json:"name"`
	EnvironmentID *int64    `json:"environment_id,omitempty"`
	ExpiresAt     *string   `json:"expires_at,omitempty"`
	Permissions   *[]string `json:"permissions,omitempty"`
}

// UpdateDeploymentTokenRequest is the body of PATCH .../deployment-tokens/{id}.
type UpdateDeploymentTokenRequest struct {
	Name        *string   `json:"name,omitempty"`
	IsActive    *bool     `json:"is_active,omitempty"`
	ExpiresAt   *string   `json:"expires_at,omitempty"`
	Permissions *[]string `json:"permissions,omitempty"`
}

// CreateDeploymentToken mints a deployment token; the response carries the
// only copy of the token value.
func (c *Client) CreateDeploymentToken(ctx context.Context, projectID int64, req CreateDeploymentTokenRequest) (*DeploymentToken, error) {
	var t DeploymentToken
	return &t, c.do(ctx, http.MethodPost, idPath("/projects/%d/deployment-tokens", projectID), nil, req, &t)
}

// GetDeploymentToken reads a deployment token (without its value).
func (c *Client) GetDeploymentToken(ctx context.Context, projectID, id int64) (*DeploymentToken, error) {
	var t DeploymentToken
	return &t, c.do(ctx, http.MethodGet, idPath("/projects/%d/deployment-tokens/%d", projectID, id), nil, nil, &t)
}

// UpdateDeploymentToken partially updates a deployment token.
func (c *Client) UpdateDeploymentToken(ctx context.Context, projectID, id int64, req UpdateDeploymentTokenRequest) (*DeploymentToken, error) {
	var t DeploymentToken
	return &t, c.do(ctx, http.MethodPatch, idPath("/projects/%d/deployment-tokens/%d", projectID, id), nil, req, &t)
}

// DeleteDeploymentToken revokes a deployment token.
func (c *Client) DeleteDeploymentToken(ctx context.Context, projectID, id int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/projects/%d/deployment-tokens/%d", projectID, id), nil, nil, nil)
}

// Team groups users for project access.
type Team struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Description *string `json:"description"`
}

// TeamRequest creates a team, or updates one (nil fields unchanged; the slug
// cannot change).
type TeamRequest struct {
	Name        *string `json:"name,omitempty"`
	Slug        *string `json:"slug,omitempty"`
	Description *string `json:"description,omitempty"`
}

// CreateTeam creates a team.
func (c *Client) CreateTeam(ctx context.Context, req TeamRequest) (*Team, error) {
	var t Team
	return &t, c.do(ctx, http.MethodPost, "/teams", nil, req, &t)
}

// GetTeam reads a team.
func (c *Client) GetTeam(ctx context.Context, id int64) (*Team, error) {
	var t Team
	return &t, c.do(ctx, http.MethodGet, idPath("/teams/%d", id), nil, nil, &t)
}

// UpdateTeam partially updates a team.
func (c *Client) UpdateTeam(ctx context.Context, id int64, req TeamRequest) (*Team, error) {
	var t Team
	return &t, c.do(ctx, http.MethodPatch, idPath("/teams/%d", id), nil, req, &t)
}

// DeleteTeam deletes a team and its project grants.
func (c *Client) DeleteTeam(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/teams/%d", id), nil, nil, nil)
}

// ProjectAccess grants a team a role on a project.
type ProjectAccess struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	TeamID    int64  `json:"team_id"`
	Role      string `json:"role"`
}

// ListProjectAccess lists a project's team grants.
func (c *Client) ListProjectAccess(ctx context.Context, projectID int64) ([]ProjectAccess, error) {
	var out []ProjectAccess
	return out, c.do(ctx, http.MethodGet, idPath("/projects/%d/access", projectID), nil, nil, &out)
}

// GetProjectAccess finds one team's grant on a project. The API has no
// single-grant GET, so it filters the list; a missing grant is a 404 APIError.
func (c *Client) GetProjectAccess(ctx context.Context, projectID, teamID int64) (*ProjectAccess, error) {
	all, err := c.ListProjectAccess(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].TeamID == teamID {
			return &all[i], nil
		}
	}
	return nil, notFoundError(http.MethodGet, idPath("/projects/%d/access", projectID), fmt.Sprintf("project %d has no access grant for team %d", projectID, teamID))
}

// GrantProjectAccess grants a team a role on a project. The API upserts: a
// second grant for the same team replaces the role.
func (c *Client) GrantProjectAccess(ctx context.Context, projectID, teamID int64, role string) (*ProjectAccess, error) {
	var a ProjectAccess
	body := map[string]any{"team_id": teamID, "role": role}
	return &a, c.do(ctx, http.MethodPost, idPath("/projects/%d/access", projectID), nil, body, &a)
}

// RevokeProjectAccess removes a team's grant on a project.
func (c *Client) RevokeProjectAccess(ctx context.Context, projectID, teamID int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/projects/%d/access/%d", projectID, teamID), nil, nil, nil)
}
