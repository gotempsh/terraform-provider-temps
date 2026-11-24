// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"net/http"
	"net/url"
)

// MaskedValue is what Temps returns in place of a stored secret.
const MaskedValue = "***"

// Service is a Temps-managed external service (database, cache, object store).
type Service struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	ServiceType  string  `json:"service_type"`
	Version      *string `json:"version"`
	Status       string  `json:"status"`
	Topology     string  `json:"topology"`
	NodeID       *int64  `json:"node_id"`
	ErrorMessage *string `json:"error_message"`
}

// ServiceDetails is GET /external-services/{id}. Sensitive parameter values
// come back masked as MaskedValue.
type ServiceDetails struct {
	Service             Service        `json:"service"`
	CurrentParameters   map[string]any `json:"current_parameters"`
	SensitiveParameters []string       `json:"sensitive_parameters"`
}

// CreateServiceRequest is the body of POST /external-services.
type CreateServiceRequest struct {
	Name        string         `json:"name"`
	ServiceType string         `json:"service_type"`
	Parameters  map[string]any `json:"parameters"`
	Topology    *string        `json:"topology,omitempty"`
	Version     *string        `json:"version,omitempty"`
	NodeID      *int64         `json:"node_id,omitempty"`
}

// ParameterSchema is the JSON schema of a service type's parameters.
type ParameterSchema struct {
	Properties map[string]ParameterProperty `json:"properties"`
	Required   []string                     `json:"required"`
}

// ParameterProperty describes one service parameter. Type is a JSON-schema
// type: a string or a list such as ["string","null"].
type ParameterProperty struct {
	Type        any    `json:"type"`
	Description string `json:"description"`
	Default     any    `json:"default"`
	Editable    *bool  `json:"x-editable"`
}

// Types returns the non-null JSON-schema types of the property.
func (p ParameterProperty) Types() []string {
	switch t := p.Type.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, v := range t {
			if s, ok := v.(string); ok && s != "null" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// CreateService starts provisioning a service. Creation is asynchronous: poll
// GetService until Status is "running".
func (c *Client) CreateService(ctx context.Context, req CreateServiceRequest) (*Service, error) {
	var s Service
	return &s, c.do(ctx, http.MethodPost, "/external-services", nil, req, &s)
}

// GetService reads a service with its (masked) parameters.
func (c *Client) GetService(ctx context.Context, id int64) (*ServiceDetails, error) {
	var d ServiceDetails
	return &d, c.do(ctx, http.MethodGet, idPath("/external-services/%d", id), nil, nil, &d)
}

// ListServices returns every service.
func (c *Client) ListServices(ctx context.Context) ([]Service, error) {
	var out []Service
	return out, c.do(ctx, http.MethodGet, "/external-services", nil, nil, &out)
}

// UpdateServiceParameters updates a service's parameters.
func (c *Client) UpdateServiceParameters(ctx context.Context, id int64, params map[string]any) (*Service, error) {
	var s Service
	body := map[string]any{"parameters": params}
	return &s, c.do(ctx, http.MethodPut, idPath("/external-services/%d", id), nil, body, &s)
}

// UpgradeService switches a service to another Docker image.
func (c *Client) UpgradeService(ctx context.Context, id int64, dockerImage string) (*Service, error) {
	var s Service
	body := map[string]string{"docker_image": dockerImage}
	return &s, c.do(ctx, http.MethodPost, idPath("/external-services/%d/upgrade", id), nil, body, &s)
}

// DeleteService deletes a service and its data.
func (c *Client) DeleteService(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/external-services/%d", id), nil, nil, nil)
}

// ListServiceTypes lists the service types the server supports.
func (c *Client) ListServiceTypes(ctx context.Context) ([]string, error) {
	var out []string
	return out, c.do(ctx, http.MethodGet, "/external-services/types", nil, nil, &out)
}

// GetServiceTypeParameters returns a service type's parameter schema.
func (c *Client) GetServiceTypeParameters(ctx context.Context, serviceType string) (*ParameterSchema, error) {
	var s ParameterSchema
	return &s, c.do(ctx, http.MethodGet, "/external-services/types/"+url.PathEscape(serviceType)+"/parameters", nil, nil, &s)
}

// ServiceLink links a service to a project.
type ServiceLink struct {
	ID                       int64   `json:"id"`
	CustomDatabaseName       *string `json:"custom_database_name"`
	DatabaseProvisioningMode string  `json:"database_provisioning_mode"`
	Project                  struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	} `json:"project"`
	Service Service `json:"service"`
}

// LinkServiceRequest is the body of POST /external-services/{id}/projects.
type LinkServiceRequest struct {
	ProjectID                int64   `json:"project_id"`
	DatabaseProvisioningMode *string `json:"database_provisioning_mode,omitempty"`
	CustomDatabaseName       *string `json:"custom_database_name,omitempty"`
}

// LinkService links a service to a project.
func (c *Client) LinkService(ctx context.Context, serviceID int64, req LinkServiceRequest) (*ServiceLink, error) {
	var l ServiceLink
	return &l, c.do(ctx, http.MethodPost, idPath("/external-services/%d/projects", serviceID), nil, req, &l)
}

// ListServiceLinks lists the projects a service is linked to.
func (c *Client) ListServiceLinks(ctx context.Context, serviceID int64) ([]ServiceLink, error) {
	var out []ServiceLink
	q := url.Values{}
	q.Set("page_size", "100")
	return out, c.do(ctx, http.MethodGet, idPath("/external-services/%d/projects", serviceID), q, nil, &out)
}

// GetServiceLink finds the link between a service and a project; a missing
// link is reported as a 404 APIError.
func (c *Client) GetServiceLink(ctx context.Context, serviceID, projectID int64) (*ServiceLink, error) {
	links, err := c.ListServiceLinks(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	for i := range links {
		if links[i].Project.ID == projectID {
			return &links[i], nil
		}
	}
	return nil, &APIError{
		StatusCode: http.StatusNotFound, Method: http.MethodGet,
		Path:   idPath("/external-services/%d/projects", serviceID),
		Title:  "Not Found",
		Detail: "service " + itoa(serviceID) + " is not linked to project " + itoa(projectID),
	}
}

// UnlinkService removes a service-project link.
func (c *Client) UnlinkService(ctx context.Context, serviceID, projectID int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/external-services/%d/projects/%d", serviceID, projectID), nil, nil, nil)
}

// ServiceVariableNames lists the names of the variables a linked service
// injects into a project. The server masks the values in this response.
func (c *Client) ServiceVariableNames(ctx context.Context, serviceID, projectID int64) ([]string, error) {
	var out map[string]string
	if err := c.do(ctx, http.MethodGet, idPath("/external-services/%d/projects/%d/environment", serviceID, projectID), nil, nil, &out); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out))
	for k := range out {
		names = append(names, k)
	}
	return names, nil
}

// ServiceVariable is one variable a linked service injects.
type ServiceVariable struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Sensitive bool   `json:"sensitive"`
}

// GetServiceVariable reveals one injected variable.
func (c *Client) GetServiceVariable(ctx context.Context, serviceID, projectID int64, name string) (*ServiceVariable, error) {
	var v ServiceVariable
	p := idPath("/external-services/%d/projects/%d/environment/", serviceID, projectID) + url.PathEscape(name)
	return &v, c.do(ctx, http.MethodGet, p, nil, nil, &v)
}
