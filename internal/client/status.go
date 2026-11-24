// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"net/http"
)

// Monitor is an uptime monitor on a project environment.
type Monitor struct {
	ID                   int64   `json:"id"`
	ProjectID            int64   `json:"project_id"`
	EnvironmentID        *int64  `json:"environment_id"`
	Name                 string  `json:"name"`
	MonitorType          string  `json:"monitor_type"`
	CheckPath            *string `json:"check_path"`
	CheckIntervalSeconds int64   `json:"check_interval_seconds"`
	MonitorURL           string  `json:"monitor_url"`
	IsActive             bool    `json:"is_active"`
	IsManaged            bool    `json:"is_managed"`
}

// CreateMonitorRequest is the body of POST /projects/{id}/monitors.
type CreateMonitorRequest struct {
	Name                 string  `json:"name"`
	MonitorType          string  `json:"monitor_type"`
	EnvironmentID        int64   `json:"environment_id"`
	CheckPath            *string `json:"check_path,omitempty"`
	CheckIntervalSeconds *int64  `json:"check_interval_seconds,omitempty"`
}

// CreateMonitor creates a monitor.
func (c *Client) CreateMonitor(ctx context.Context, projectID int64, req CreateMonitorRequest) (*Monitor, error) {
	var m Monitor
	return &m, c.do(ctx, http.MethodPost, idPath("/projects/%d/monitors", projectID), nil, req, &m)
}

// GetMonitor reads a monitor.
func (c *Client) GetMonitor(ctx context.Context, id int64) (*Monitor, error) {
	var m Monitor
	return &m, c.do(ctx, http.MethodGet, idPath("/monitors/%d", id), nil, nil, &m)
}

// UpdateMonitorCheckPath changes the path a monitor probes (the only field
// the API updates in place).
func (c *Client) UpdateMonitorCheckPath(ctx context.Context, id int64, checkPath string) (*Monitor, error) {
	var m Monitor
	body := map[string]string{"check_path": checkPath}
	return &m, c.do(ctx, http.MethodPatch, idPath("/monitors/%d", id), nil, body, &m)
}

// DeleteMonitor deletes a monitor.
func (c *Client) DeleteMonitor(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/monitors/%d", id), nil, nil, nil)
}

// StatusPageMonitor places a monitor on a status page.
type StatusPageMonitor struct {
	MonitorID   int64   `json:"monitor_id"`
	DisplayName string  `json:"display_name"`
	GroupName   *string `json:"group_name"`
	Position    int64   `json:"position,omitempty"`
}

// StatusPageAppearance is how a status page looks.
type StatusPageAppearance struct {
	Theme                *string `json:"theme,omitempty"`
	AccentColor          *string `json:"accent_color,omitempty"`
	LogoURL              *string `json:"logo_url,omitempty"`
	WebsiteURL           *string `json:"website_url,omitempty"`
	ShowUptimePercentage *bool   `json:"show_uptime_percentage,omitempty"`
	ShowResponseTimes    *bool   `json:"show_response_times,omitempty"`
}

// StatusPage is a public status page.
type StatusPage struct {
	ID              int64                `json:"id"`
	Title           string               `json:"title"`
	Description     *string              `json:"description"`
	Slug            string               `json:"slug"`
	IsPublished     bool                 `json:"is_published"`
	PublicURL       *string              `json:"public_url"`
	PublicJSONURL   *string              `json:"public_json_url"`
	Monitors        []StatusPageMonitor  `json:"monitors"`
	Appearance      StatusPageAppearance `json:"appearance"`
	PublicURLStatus struct {
		Configured bool    `json:"configured"`
		Reason     *string `json:"reason"`
	} `json:"public_url_status"`
}

// StatusPageRequest creates or updates a status page. On update nil fields
// are unchanged; Monitors, when set, replaces the whole ordered list.
type StatusPageRequest struct {
	Title       *string               `json:"title,omitempty"`
	Description *string               `json:"description,omitempty"`
	Monitors    *[]StatusPageMonitor  `json:"monitors,omitempty"`
	Appearance  *StatusPageAppearance `json:"appearance,omitempty"`
}

// CreateStatusPage creates an unpublished status page.
func (c *Client) CreateStatusPage(ctx context.Context, req StatusPageRequest) (*StatusPage, error) {
	var p StatusPage
	if req.Monitors == nil {
		req.Monitors = &[]StatusPageMonitor{}
	}
	return &p, c.do(ctx, http.MethodPost, "/status-pages", nil, req, &p)
}

// GetStatusPage reads a status page.
func (c *Client) GetStatusPage(ctx context.Context, id int64) (*StatusPage, error) {
	var p StatusPage
	return &p, c.do(ctx, http.MethodGet, idPath("/status-pages/%d", id), nil, nil, &p)
}

// UpdateStatusPage partially updates a status page.
func (c *Client) UpdateStatusPage(ctx context.Context, id int64, req StatusPageRequest) (*StatusPage, error) {
	var p StatusPage
	return &p, c.do(ctx, http.MethodPatch, idPath("/status-pages/%d", id), nil, req, &p)
}

// SetStatusPagePublished publishes or unpublishes a status page.
func (c *Client) SetStatusPagePublished(ctx context.Context, id int64, published bool) (*StatusPage, error) {
	var p StatusPage
	action := "unpublish"
	if published {
		action = "publish"
	}
	return &p, c.do(ctx, http.MethodPost, idPath("/status-pages/%d/", id)+action, nil, nil, &p)
}

// DeleteStatusPage deletes a status page.
func (c *Client) DeleteStatusPage(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/status-pages/%d", id), nil, nil, nil)
}

// DNSRecord is a DNS record to create for a custom status page domain.
type DNSRecord struct {
	RecordType string `json:"record_type"`
	Name       string `json:"name"`
	Value      string `json:"value"`
	Purpose    string `json:"purpose"`
}

// StatusPageDomain is a custom hostname for a status page.
type StatusPageDomain struct {
	ID                 int64       `json:"id"`
	Hostname           string      `json:"hostname"`
	VerificationMethod string      `json:"verification_method"`
	Status             string      `json:"status"`
	Serving            bool        `json:"serving"`
	LastError          *string     `json:"last_error"`
	DNSRecords         []DNSRecord `json:"dns_records"`
}

// AddStatusPageDomain attaches a custom hostname.
func (c *Client) AddStatusPageDomain(ctx context.Context, pageID int64, hostname string, method *string) (*StatusPageDomain, error) {
	var d StatusPageDomain
	body := struct {
		Hostname           string  `json:"hostname"`
		VerificationMethod *string `json:"verification_method,omitempty"`
	}{hostname, method}
	return &d, c.do(ctx, http.MethodPost, idPath("/status-pages/%d/domains", pageID), nil, body, &d)
}

// GetStatusPageDomain finds a domain by ID (list and filter); a missing
// domain is reported as a 404 APIError.
func (c *Client) GetStatusPageDomain(ctx context.Context, pageID, domainID int64) (*StatusPageDomain, error) {
	var out []StatusPageDomain
	if err := c.do(ctx, http.MethodGet, idPath("/status-pages/%d/domains", pageID), nil, nil, &out); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].ID == domainID {
			return &out[i], nil
		}
	}
	return nil, &APIError{
		StatusCode: http.StatusNotFound, Method: http.MethodGet,
		Path:   idPath("/status-pages/%d/domains", pageID),
		Title:  "Not Found",
		Detail: "domain " + itoa(domainID) + " not found on status page " + itoa(pageID),
	}
}

// VerifyStatusPageDomain asks the server to verify DNS for a domain now.
func (c *Client) VerifyStatusPageDomain(ctx context.Context, pageID, domainID int64) (*StatusPageDomain, error) {
	var d StatusPageDomain
	return &d, c.do(ctx, http.MethodPost, idPath("/status-pages/%d/domains/%d/verify", pageID, domainID), nil, nil, &d)
}

// DeleteStatusPageDomain detaches a custom hostname.
func (c *Client) DeleteStatusPageDomain(ctx context.Context, pageID, domainID int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/status-pages/%d/domains/%d", pageID, domainID), nil, nil, nil)
}

// SetStatusPagePassword sets (or, with an empty password, clears) the
// password protecting a status page. Servers older than the endpoint return
// an error for which IsUnsupported is true.
func (c *Client) SetStatusPagePassword(ctx context.Context, id int64, password string) error {
	body := map[string]*string{"password": nil}
	if password != "" {
		body["password"] = &password
	}
	return c.do(ctx, http.MethodPut, idPath("/status-pages/%d/password", id), nil, body, nil)
}

// StatusPageSubscriptions are a status page's email subscription settings.
type StatusPageSubscriptions struct {
	Enabled bool    `json:"enabled"`
	Sender  *string `json:"sender,omitempty"`
}

// GetStatusPageSubscriptions reads subscription settings.
func (c *Client) GetStatusPageSubscriptions(ctx context.Context, id int64) (*StatusPageSubscriptions, error) {
	var s StatusPageSubscriptions
	return &s, c.do(ctx, http.MethodGet, idPath("/status-pages/%d/subscriptions", id), nil, nil, &s)
}

// UpdateStatusPageSubscriptions updates subscription settings.
func (c *Client) UpdateStatusPageSubscriptions(ctx context.Context, id int64, s StatusPageSubscriptions) (*StatusPageSubscriptions, error) {
	var out StatusPageSubscriptions
	return &out, c.do(ctx, http.MethodPut, idPath("/status-pages/%d/subscriptions", id), nil, s, &out)
}
