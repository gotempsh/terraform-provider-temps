// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// ProjectGroupMember is a project as listed inside a project group.
type ProjectGroupMember struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// ProjectGroup groups projects that share environment variables.
type ProjectGroup struct {
	ID          int64                `json:"id"`
	Name        string               `json:"name"`
	Slug        string               `json:"slug"`
	Description *string              `json:"description"`
	Projects    []ProjectGroupMember `json:"projects"`
}

// HasProject reports whether the group lists the project.
func (g *ProjectGroup) HasProject(projectID int64) bool {
	for _, p := range g.Projects {
		if p.ID == projectID {
			return true
		}
	}
	return false
}

// ProjectGroupRequest creates or partially updates a project group.
type ProjectGroupRequest struct {
	Name        *string `json:"name,omitempty"`
	Slug        *string `json:"slug,omitempty"`
	Description *string `json:"description,omitempty"`
}

// CreateProjectGroup creates a project group.
func (c *Client) CreateProjectGroup(ctx context.Context, req ProjectGroupRequest) (*ProjectGroup, error) {
	var g ProjectGroup
	return &g, c.do(ctx, http.MethodPost, "/project-groups", nil, req, &g)
}

// GetProjectGroup reads a project group with its member projects.
func (c *Client) GetProjectGroup(ctx context.Context, id int64) (*ProjectGroup, error) {
	var g ProjectGroup
	return &g, c.do(ctx, http.MethodGet, idPath("/project-groups/%d", id), nil, nil, &g)
}

// GetProjectGroupBySlug reads a project group by its slug.
func (c *Client) GetProjectGroupBySlug(ctx context.Context, slug string) (*ProjectGroup, error) {
	var g ProjectGroup
	return &g, c.do(ctx, http.MethodGet, "/project-groups/by-slug/"+url.PathEscape(slug), nil, nil, &g)
}

// UpdateProjectGroup partially updates a project group.
func (c *Client) UpdateProjectGroup(ctx context.Context, id int64, req ProjectGroupRequest) (*ProjectGroup, error) {
	var g ProjectGroup
	return &g, c.do(ctx, http.MethodPatch, idPath("/project-groups/%d", id), nil, req, &g)
}

// DeleteProjectGroup deletes a project group; its projects are kept.
func (c *Client) DeleteProjectGroup(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/project-groups/%d", id), nil, nil, nil)
}

// FindProjectGroupOf returns the group that lists the project, or nil. The
// API has no reverse lookup, so it pages through every group.
func (c *Client) FindProjectGroupOf(ctx context.Context, projectID int64) (*ProjectGroup, error) {
	for page := 1; page <= 1000; page++ {
		var out struct {
			Groups   []ProjectGroup `json:"groups"`
			Total    int64          `json:"total"`
			PageSize int64          `json:"page_size"`
		}
		q := url.Values{"page": {fmt.Sprint(page)}, "page_size": {"100"}}
		if err := c.do(ctx, http.MethodGet, "/project-groups", q, nil, &out); err != nil {
			return nil, err
		}
		for i := range out.Groups {
			if out.Groups[i].HasProject(projectID) {
				return &out.Groups[i], nil
			}
		}
		if len(out.Groups) == 0 || int64(page)*int64(len(out.Groups)) >= out.Total {
			return nil, nil
		}
	}
	return nil, nil
}

// AddProjectToGroup adds a project to a group. The API moves a project that
// is in another group without saying so.
func (c *Client) AddProjectToGroup(ctx context.Context, groupID, projectID int64) error {
	return c.do(ctx, http.MethodPut, idPath("/project-groups/%d/projects/%d", groupID, projectID), nil, nil, nil)
}

// RemoveProjectFromGroup removes a project from a group.
func (c *Client) RemoveProjectFromGroup(ctx context.Context, groupID, projectID int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/project-groups/%d/projects/%d", groupID, projectID), nil, nil, nil)
}

// ProjectGroupVariable is an environment variable shared by a group's projects.
type ProjectGroupVariable struct {
	ID               int64   `json:"id"`
	Key              string  `json:"key"`
	Value            *string `json:"value"`
	EnvironmentSlug  *string `json:"environment_slug"`
	IncludeInPreview bool    `json:"include_in_preview"`
	IsSecret         bool    `json:"is_secret"`
}

// CreateProjectGroupVariableRequest is the body of POST .../env-vars.
type CreateProjectGroupVariableRequest struct {
	Key              string  `json:"key"`
	Value            string  `json:"value"`
	EnvironmentSlug  *string `json:"environment_slug,omitempty"`
	IncludeInPreview bool    `json:"include_in_preview"`
	IsSecret         bool    `json:"is_secret"`
}

// UpdateProjectGroupVariableRequest is the body of PATCH .../env-vars/{id}.
type UpdateProjectGroupVariableRequest struct {
	Value            *string `json:"value,omitempty"`
	IncludeInPreview *bool   `json:"include_in_preview,omitempty"`
	IsSecret         *bool   `json:"is_secret,omitempty"`
}

// ListProjectGroupVariables lists a group's variables (secret values are null).
func (c *Client) ListProjectGroupVariables(ctx context.Context, groupID int64) ([]ProjectGroupVariable, error) {
	var out []ProjectGroupVariable
	return out, c.do(ctx, http.MethodGet, idPath("/project-groups/%d/env-vars", groupID), nil, nil, &out)
}

// GetProjectGroupVariable finds one variable. The API has no single-variable
// GET, so it filters the list; a missing variable is a 404 APIError.
func (c *Client) GetProjectGroupVariable(ctx context.Context, groupID, id int64) (*ProjectGroupVariable, error) {
	all, err := c.ListProjectGroupVariables(ctx, groupID)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, notFoundError(idPath("/project-groups/%d/env-vars", groupID), fmt.Sprintf("project group %d has no variable %d", groupID, id))
}

// CreateProjectGroupVariable creates a group variable.
func (c *Client) CreateProjectGroupVariable(ctx context.Context, groupID int64, req CreateProjectGroupVariableRequest) (*ProjectGroupVariable, error) {
	var v ProjectGroupVariable
	return &v, c.do(ctx, http.MethodPost, idPath("/project-groups/%d/env-vars", groupID), nil, req, &v)
}

// UpdateProjectGroupVariable partially updates a group variable.
func (c *Client) UpdateProjectGroupVariable(ctx context.Context, groupID, id int64, req UpdateProjectGroupVariableRequest) (*ProjectGroupVariable, error) {
	var v ProjectGroupVariable
	return &v, c.do(ctx, http.MethodPatch, idPath("/project-groups/%d/env-vars/%d", groupID, id), nil, req, &v)
}

// DeleteProjectGroupVariable deletes a group variable.
func (c *Client) DeleteProjectGroupVariable(ctx context.Context, groupID, id int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/project-groups/%d/env-vars/%d", groupID, id), nil, nil, nil)
}
