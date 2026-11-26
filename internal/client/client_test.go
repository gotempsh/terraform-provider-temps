// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "tk_test_0123456789"

func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{Endpoint: srv.URL, APIKey: testKey, MinBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewValidatesConfig(t *testing.T) {
	cases := map[string]Config{
		"empty endpoint": {APIKey: testKey},
		"empty key":      {Endpoint: "https://temps.example.com"},
		"bad scheme":     {Endpoint: "ftp://temps.example.com", APIKey: testKey},
		"no host":        {Endpoint: "https://", APIKey: testKey},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(cfg); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestNewAppendsAPIPath(t *testing.T) {
	for in, want := range map[string]string{
		"https://temps.example.com":          "https://temps.example.com/api",
		"https://temps.example.com/":         "https://temps.example.com/api",
		"https://temps.example.com/api":      "https://temps.example.com/api",
		"https://temps.example.com/api/":     "https://temps.example.com/api",
		"https://example.com/temps?x=1#frag": "https://example.com/temps/api",
		" http://127.0.0.1:8080 ":            "http://127.0.0.1:8080/api", //nolint:gocritic // surrounding whitespace is what this case tests
	} {
		c, err := New(Config{Endpoint: in, APIKey: testKey})
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got := c.BaseURL(); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestRequestCarriesBearerAndJSON(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testKey {
			t.Errorf("authorization header = %q", got)
		}
		if r.URL.Path != "/api/projects" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q", ct)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "web" {
			t.Errorf("body = %v", body)
		}
		_, _ = io.WriteString(w, `{"id":7,"slug":"web","name":"web","source_type":"git","deployment_config":{"exposedPort":3000}}`)
	}))
	p, err := c.CreateProject(context.Background(), CreateProjectRequest{Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != 7 || p.DeploymentConfig.ExposedPort == nil || *p.DeploymentConfig.ExposedPort != 3000 {
		t.Fatalf("unexpected project %+v", p)
	}
}

func TestProblemDetailsAreParsed(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"type":"https://temps.sh/probs/not-found","title":"Project Not Found","detail":"project 42 not found","error_code":"NOT_FOUND"}`)
	}))
	_, err := c.GetProject(context.Background(), 42)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T %v", err, err)
	}
	if !IsNotFound(err) || apiErr.Title != "Project Not Found" || apiErr.Detail != "project 42 not found" || apiErr.ErrorCode != "NOT_FOUND" {
		t.Fatalf("unexpected error %+v", apiErr)
	}
	if !strings.Contains(err.Error(), "GET /projects/42: HTTP 404 Project Not Found: project 42 not found") {
		t.Fatalf("error message %q", err.Error())
	}
}

func TestForbiddenNamesPermission(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"title":"Insufficient Permissions","detail":"This operation requires the backups:create permission","required_permission":"backups:create"}`)
	}))
	_, err := c.CreateBackupSchedule(context.Background(), CreateBackupScheduleRequest{Name: "nightly"})
	if !IsForbidden(err) {
		t.Fatalf("expected 403, got %v", err)
	}
	var apiErr *APIError
	errors.As(err, &apiErr)
	if apiErr.RequiredPermission != "backups:create" || !strings.Contains(err.Error(), `missing the "backups:create" permission`) {
		t.Fatalf("unexpected %v", err)
	}
}

func TestPlainTextErrorBodyIsKept(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, "Failed to deserialize the JSON body: unknown variant `system`")
	}))
	_, err := c.CreateStatusPage(context.Background(), StatusPageRequest{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(apiErr.Body, "unknown variant") {
		t.Fatalf("unexpected %v", err)
	}
}

func TestRetriesTooManyRequestsForPost(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":1,"name":"production"}`)
	}))
	env, err := c.CreateEnvironment(context.Background(), 1, CreateEnvironmentRequest{Name: "staging", Branch: "main"})
	if err != nil || env.ID != 1 || calls.Load() != 3 {
		t.Fatalf("env=%+v err=%v calls=%d", env, err, calls.Load())
	}
}

func TestGatewayErrorsRetryOnlyIdempotentMethods(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	if _, err := c.CreateProject(context.Background(), CreateProjectRequest{Name: "web"}); err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Fatalf("POST was retried %d times; a 502 must not be retried for non-idempotent requests", calls.Load()-1)
	}
	calls.Store(0)
	if _, err := c.GetProject(context.Background(), 1); err == nil {
		t.Fatal("expected error")
	}
	if want := int32(DefaultMaxRetries + 1); calls.Load() != want {
		t.Fatalf("GET attempts = %d, want %d", calls.Load(), want)
	}
}

func TestRetriesConflictThatAsksForRetry(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 2 {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"title":"Group Members Changed","detail":"1 project(s) joined project group 1 while this request was being authorized; retry the request"}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":1,"name":"web"}`)
	}))
	if _, err := c.CreateProject(context.Background(), CreateProjectRequest{Name: "web"}); err != nil || calls.Load() != 2 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}

	// Any other conflict is final.
	calls.Store(0)
	c = newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"title":"Conflict","detail":"A project named web already exists"}`)
	}))
	if _, err := c.CreateProject(context.Background(), CreateProjectRequest{Name: "web"}); !IsConflict(err) || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestDoesNotRetryClientErrors(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"title":"Validation Error","detail":"Invalid backup schedule"}`)
	}))
	_, err := c.GetBackupSchedule(context.Background(), 3)
	if err == nil || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestRetriesRefusedConnections(t *testing.T) {
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close() // nothing listens: every dial is refused
	c, err := New(Config{Endpoint: "http://" + addr, APIKey: testKey, MaxRetries: 2, MinBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CreateProject(context.Background(), CreateProjectRequest{Name: "web"})
	if err == nil || !strings.Contains(err.Error(), "POST /projects") {
		t.Fatalf("unexpected %v", err)
	}
}

func TestContextCancellationStopsRetries(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	c.minBackoff, c.maxBackoff = time.Hour, time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.GetProject(ctx, 1)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(start))
	}
}

func TestErrorNeverContainsAPIKey(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"title":"Unauthorized","detail":"invalid token"}`)
	}))
	_, err := c.GetProject(context.Background(), 1)
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Fatalf("unexpected %v", err)
	}
}

func TestGetEnvironmentVariableFiltersList(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"id":1,"key":"API_TOKEN","value":null,"is_secret":true,"environments":[{"id":2,"name":"production"}]},{"id":2,"key":"LOG_LEVEL","value":"***","is_secret":false,"environments":[]}]`)
	}))
	v, err := c.GetEnvironmentVariable(context.Background(), 9, 1)
	if err != nil || v.Key != "API_TOKEN" || v.Value != nil || len(v.Environments) != 1 {
		t.Fatalf("v=%+v err=%v", v, err)
	}
	if _, err := c.GetEnvironmentVariable(context.Background(), 9, 77); !IsNotFound(err) {
		t.Fatalf("expected 404 for a missing variable, got %v", err)
	}
}

func TestGetEnvironmentVariableValueUsesVarID(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects/9/env-vars/LOG_LEVEL/value" || r.URL.Query().Get("var_id") != "2" {
			t.Errorf("unexpected %s", r.URL.String())
		}
		_, _ = io.WriteString(w, `{"value":"info"}`)
	}))
	v, err := c.GetEnvironmentVariableValue(context.Background(), 9, 2, "LOG_LEVEL")
	if err != nil || v != "info" {
		t.Fatalf("v=%q err=%v", v, err)
	}
}

func TestServiceVariableNamesReadsMap(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"POSTGRES_HOST":"***","POSTGRES_PASSWORD":"***"}`)
	}))
	names, err := c.ServiceVariableNames(context.Background(), 1, 2)
	if err != nil || len(names) != 2 {
		t.Fatalf("names=%v err=%v", names, err)
	}
}

func TestGetServiceLinkMissingIsNotFound(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"id":1,"project":{"id":5,"slug":"web"},"service":{"id":3},"database_provisioning_mode":"project"}]`)
	}))
	if l, err := c.GetServiceLink(context.Background(), 3, 5); err != nil || l.ID != 1 {
		t.Fatalf("l=%+v err=%v", l, err)
	}
	if _, err := c.GetServiceLink(context.Background(), 3, 6); !IsNotFound(err) {
		t.Fatalf("expected 404, got %v", err)
	}
}

func TestParameterPropertyTypes(t *testing.T) {
	var s ParameterSchema
	if err := json.Unmarshal([]byte(`{"properties":{"port":{"type":["string","null"]},"max_connections":{"type":"integer"}}}`), &s); err != nil {
		t.Fatal(err)
	}
	if got := s.Properties["port"].Types(); len(got) != 1 || got[0] != "string" {
		t.Fatalf("port types %v", got)
	}
	if got := s.Properties["max_connections"].Types(); len(got) != 1 || got[0] != "integer" {
		t.Fatalf("max_connections types %v", got)
	}
}

func TestNoContentResponse(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if err := c.DeleteMonitor(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
}

func TestIsUnsupportedDistinguishesMissingRoutes(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/password") {
			w.WriteHeader(http.StatusNotFound) // unknown route: no body
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"title":"Resource Not Found","detail":"status page 1 not found"}`)
	}))
	err := c.SetStatusPagePassword(context.Background(), 1, "pw")
	if !IsUnsupported(err) {
		t.Fatalf("expected unsupported, got %v", err)
	}
	_, err = c.GetStatusPage(context.Background(), 1)
	if IsUnsupported(err) || !IsNotFound(err) {
		t.Fatalf("a missing resource must not look unsupported: %v", err)
	}
}

// Requests that restart a service before Temps answers get the longer
// SlowTimeout; every other request keeps Timeout.
func TestServiceRestartingRequestsUseSlowTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"tls":{"mode":"available"}}`)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{Endpoint: srv.URL, APIKey: testKey, Timeout: 50 * time.Millisecond,
		SlowTimeout: 5 * time.Second, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.ApplyServiceHealthcheck(ctx, 1); err != nil {
		t.Fatalf("applying a healthcheck timed out under Timeout: %v", err)
	}
	if tls, err := c.SetServiceTLS(ctx, 1, "available"); err != nil || tls.Mode != "available" {
		t.Fatalf("changing the TLS mode: %+v, %v", tls, err)
	}
	if _, err := c.GetServiceTLS(ctx, 1); err == nil || !strings.Contains(err.Error(), "Timeout") {
		t.Fatalf("a regular request should time out after Timeout, got %v", err)
	}
}

func TestSlowTimeoutIsNeverShorterThanTimeout(t *testing.T) {
	for _, tc := range []struct{ timeout, slow, want time.Duration }{
		{0, 0, DefaultSlowTimeout},
		{time.Minute, 10 * time.Second, time.Minute},
		{10 * time.Minute, 0, 10 * time.Minute},
		{time.Minute, 5 * time.Minute, 5 * time.Minute},
	} {
		c, err := New(Config{Endpoint: "https://temps.example.com", APIKey: testKey, Timeout: tc.timeout, SlowTimeout: tc.slow})
		if err != nil {
			t.Fatal(err)
		}
		if c.slow.Timeout != tc.want {
			t.Errorf("Timeout %v, SlowTimeout %v: slow timeout is %v, want %v", tc.timeout, tc.slow, c.slow.Timeout, tc.want)
		}
	}
}
