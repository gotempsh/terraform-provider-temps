// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"net/http"
)

// HealthcheckOverrides are a managed service's own Docker healthcheck values.
// A nil value inherits the instance default (or the engine's built-in one).
// Every field is always sent: PUT replaces the whole set, and null clears.
type HealthcheckOverrides struct {
	IntervalSecs      *int64 `json:"interval_secs"`
	StartIntervalSecs *int64 `json:"start_interval_secs"`
	StartPeriodSecs   *int64 `json:"start_period_secs"`
	TimeoutSecs       *int64 `json:"timeout_secs"`
	Retries           *int64 `json:"retries"`
}

// HealthcheckCapability says whether a service's container takes overrides
// and whether Temps can recreate it now to apply them.
type HealthcheckCapability struct {
	Configurable   bool    `json:"configurable"`
	Reason         *string `json:"reason"`
	CanApplyNow    bool    `json:"can_apply_now"`
	ApplyNowReason *string `json:"apply_now_reason"`
}

// ServiceHealthcheck is GET /external-services/{id}/healthcheck.
type ServiceHealthcheck struct {
	ServiceID       int64                 `json:"service_id"`
	Overrides       HealthcheckOverrides  `json:"overrides"`
	Capability      HealthcheckCapability `json:"capability"`
	PendingRecreate bool                  `json:"pending_recreate"`
}

func servicePath(id int64, suffix string) string {
	return idPath("/external-services/%d", id) + suffix
}

// GetServiceHealthcheck reads a service's healthcheck overrides.
func (c *Client) GetServiceHealthcheck(ctx context.Context, id int64) (*ServiceHealthcheck, error) {
	var out ServiceHealthcheck
	return &out, c.do(ctx, http.MethodGet, servicePath(id, "/healthcheck"), nil, nil, &out)
}

// SetServiceHealthcheck replaces a service's healthcheck overrides. Nothing
// running changes: they apply when the container is next recreated.
func (c *Client) SetServiceHealthcheck(ctx context.Context, id int64, o HealthcheckOverrides) (*ServiceHealthcheck, error) {
	var out ServiceHealthcheck
	return &out, c.do(ctx, http.MethodPut, servicePath(id, "/healthcheck"), nil, o, &out)
}

// ApplyServiceHealthcheck recreates the service's container so its
// healthcheck takes effect now. The service restarts briefly.
func (c *Client) ApplyServiceHealthcheck(ctx context.Context, id int64) error {
	return c.doSlow(ctx, http.MethodPost, servicePath(id, "/healthcheck/apply"), nil, nil, nil)
}

// ServiceTLS is GET /external-services/{id}/tls (the fields the provider uses).
type ServiceTLS struct {
	Mode                   string  `json:"mode"`
	Supported              bool    `json:"supported"`
	UnsupportedReason      *string `json:"unsupported_reason"`
	ChangeRestartsDatabase bool    `json:"change_restarts_database"`
	PendingRestart         bool    `json:"pending_restart"`
}

// GetServiceTLS reads a service's server TLS mode.
func (c *Client) GetServiceTLS(ctx context.Context, id int64) (*ServiceTLS, error) {
	var out ServiceTLS
	return &out, c.do(ctx, http.MethodGet, servicePath(id, "/tls"), nil, nil, &out)
}

// SetServiceTLS changes a service's server TLS mode. A running standalone
// database restarts to apply it; the call returns once it has.
func (c *Client) SetServiceTLS(ctx context.Context, id int64, mode string) (*ServiceTLS, error) {
	var out struct {
		TLS ServiceTLS `json:"tls"`
	}
	err := c.doSlow(ctx, http.MethodPut, servicePath(id, "/tls"), nil, map[string]string{"mode": mode}, &out)
	return &out.TLS, err
}

// ServiceLogIngestion is a service's own container log budget. Nil inherits
// the platform budget; 0 lifts the limit. Both fields are always sent.
type ServiceLogIngestion struct {
	LinesPerSec *int64 `json:"log_ingest_lines_per_sec"`
	KiBPerSec   *int64 `json:"log_ingest_kib_per_sec"`
}

// GetServiceLogIngestion reads a service's log ingestion budget.
func (c *Client) GetServiceLogIngestion(ctx context.Context, id int64) (*ServiceLogIngestion, error) {
	var out ServiceLogIngestion
	return &out, c.do(ctx, http.MethodGet, servicePath(id, "/log-ingestion"), nil, nil, &out)
}

// SetServiceLogIngestion replaces a service's log ingestion budget. Running
// containers pick it up within a minute; nothing restarts.
func (c *Client) SetServiceLogIngestion(ctx context.Context, id int64, l ServiceLogIngestion) (*ServiceLogIngestion, error) {
	var out ServiceLogIngestion
	return &out, c.do(ctx, http.MethodPut, servicePath(id, "/log-ingestion"), nil, l, &out)
}

// ServiceWorkloadMode is GET /external-services/{id}/workload-mode.
type ServiceWorkloadMode struct {
	Mode    string `json:"mode"`
	Support struct {
		Supported bool    `json:"supported"`
		Reason    *string `json:"reason"`
	} `json:"support"`
}

// GetServiceWorkloadMode reads a database's workload mode. Temps requires an
// instance administrator.
func (c *Client) GetServiceWorkloadMode(ctx context.Context, id int64) (*ServiceWorkloadMode, error) {
	var out ServiceWorkloadMode
	return &out, c.do(ctx, http.MethodGet, servicePath(id, "/workload-mode"), nil, nil, &out)
}

// SetServiceWorkloadMode changes a database's workload mode. Nothing running
// changes. Temps requires an instance administrator.
func (c *Client) SetServiceWorkloadMode(ctx context.Context, id int64, mode string) (*ServiceWorkloadMode, error) {
	var out struct {
		Workload ServiceWorkloadMode `json:"workload"`
	}
	err := c.do(ctx, http.MethodPut, servicePath(id, "/workload-mode"), nil, map[string]string{"mode": mode}, &out)
	return &out.Workload, err
}
