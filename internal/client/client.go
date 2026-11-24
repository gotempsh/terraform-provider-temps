// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package client is a small, typed client for the subset of the Temps API
// that the Terraform provider manages. It is written by hand against the
// Temps OpenAPI document and deliberately covers only the endpoints the
// provider uses.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultTimeout bounds a single HTTP request, including reading the body.
	DefaultTimeout = 60 * time.Second
	// DefaultMaxRetries is how many times a retryable failure is retried.
	DefaultMaxRetries = 4

	defaultMinBackoff = 500 * time.Millisecond
	defaultMaxBackoff = 8 * time.Second
	maxErrorBody      = 64 << 10
)

// Config configures a Client.
type Config struct {
	// Endpoint is the Temps base URL, for example https://temps.example.com.
	// The client appends /api unless the URL already ends with it.
	Endpoint string
	// APIKey is a Temps API key (tk_...). It is sent as a bearer token and
	// never logged.
	APIKey string
	// UserAgent is sent with every request.
	UserAgent string
	// Timeout bounds each request. Zero uses DefaultTimeout.
	Timeout time.Duration
	// MaxRetries bounds retries of retryable failures. Negative disables
	// retries; zero uses DefaultMaxRetries.
	MaxRetries int
	// HTTPClient overrides the underlying HTTP client (tests).
	HTTPClient *http.Client
	// MinBackoff and MaxBackoff bound the exponential backoff (tests).
	MinBackoff, MaxBackoff time.Duration
}

// Client talks to the Temps API.
type Client struct {
	base       *url.URL
	apiKey     string
	userAgent  string
	http       *http.Client
	maxRetries int
	minBackoff time.Duration
	maxBackoff time.Duration
}

// New validates cfg and returns a Client.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, errors.New("temps endpoint is empty: set the provider's endpoint or TEMPS_ENDPOINT")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("temps API key is empty: set the provider's api_key or TEMPS_API_KEY")
	}
	u, err := url.Parse(strings.TrimSpace(cfg.Endpoint))
	if err != nil {
		return nil, fmt.Errorf("temps endpoint %q is not a valid URL: %w", cfg.Endpoint, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("temps endpoint %q must use http or https", cfg.Endpoint)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("temps endpoint %q has no host", cfg.Endpoint)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/api") {
		u.Path += "/api"
	}
	u.RawQuery, u.Fragment = "", ""

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	retries := cfg.MaxRetries
	switch {
	case retries < 0:
		retries = 0
	case retries == 0:
		retries = DefaultMaxRetries
	}
	minB, maxB := cfg.MinBackoff, cfg.MaxBackoff
	if minB <= 0 {
		minB = defaultMinBackoff
	}
	if maxB <= 0 {
		maxB = defaultMaxBackoff
	}
	ua := cfg.UserAgent
	if ua == "" {
		ua = "terraform-provider-temps"
	}
	return &Client{base: u, apiKey: cfg.APIKey, userAgent: ua, http: hc, maxRetries: retries, minBackoff: minB, maxBackoff: maxB}, nil
}

// BaseURL returns the API base URL (ending in /api).
func (c *Client) BaseURL() string { return c.base.String() }

// APIError is a non-2xx response. Temps returns RFC 7807 problem details;
// Title, Detail, Type and RequiredPermission are copied from it when present.
type APIError struct {
	StatusCode         int
	Method             string
	Path               string
	Title              string
	Detail             string
	Type               string
	ErrorCode          string
	RequiredPermission string
	// Body holds the raw body when it was not problem+json.
	Body string
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
	if e.Title != "" {
		fmt.Fprintf(&b, " %s", e.Title)
	}
	if e.Detail != "" {
		fmt.Fprintf(&b, ": %s", e.Detail)
	} else if e.Body != "" {
		fmt.Fprintf(&b, ": %s", e.Body)
	}
	if e.RequiredPermission != "" {
		fmt.Fprintf(&b, " (API key is missing the %q permission)", e.RequiredPermission)
	}
	return b.String()
}

// IsNotFound reports whether err is an HTTP 404 from Temps.
func IsNotFound(err error) bool { return statusIs(err, http.StatusNotFound) }

// IsUnsupported reports whether err looks like the server does not have the
// endpoint at all: a 404 or 405 without a problem-details body. Temps answers
// unknown routes that way, while a missing resource carries problem details.
func IsUnsupported(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode != http.StatusNotFound && apiErr.StatusCode != http.StatusMethodNotAllowed {
		return false
	}
	return apiErr.Detail == "" && apiErr.Body == ""
}

// IsForbidden reports whether err is an HTTP 403 from Temps.
func IsForbidden(err error) bool { return statusIs(err, http.StatusForbidden) }

// IsConflict reports whether err is an HTTP 409 from Temps.
func IsConflict(err error) bool { return statusIs(err, http.StatusConflict) }

func statusIs(err error, code int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == code
}

type problem struct {
	Type               string `json:"type"`
	Title              string `json:"title"`
	Detail             string `json:"detail"`
	ErrorCode          string `json:"error_code"`
	RequiredPermission string `json:"required_permission"`
}

// do sends one logical request, retrying retryable failures, and decodes a
// 2xx JSON body into out (when out is non-nil).
func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var payload []byte
	if in != nil {
		var err error
		payload, err = json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encoding %s %s request body: %w", method, path, err)
		}
	}
	u := *c.base
	u.Path = c.base.Path + path
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}

	for attempt := 0; ; attempt++ {
		resp, err := c.send(ctx, method, u.String(), payload)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("%s %s: %w", method, path, ctx.Err())
			}
			if attempt < c.maxRetries && retryableTransportError(method, err) {
				if werr := c.wait(ctx, attempt, 0); werr != nil {
					return fmt.Errorf("%s %s: %w (after: %w)", method, path, werr, err)
				}
				continue
			}
			return fmt.Errorf("%s %s: %w", method, path, err)
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			defer resp.Body.Close()
			if out == nil || resp.StatusCode == http.StatusNoContent {
				_, _ = io.Copy(io.Discard, resp.Body)
				return nil
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return fmt.Errorf("%s %s: reading response: %w", method, path, err)
			}
			if len(bytes.TrimSpace(body)) == 0 {
				return nil
			}
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("%s %s: decoding response: %w", method, path, err)
			}
			return nil
		}

		apiErr := readAPIError(resp, method, path)
		if attempt < c.maxRetries && retryableStatus(method, resp.StatusCode) {
			if werr := c.wait(ctx, attempt, retryAfter(resp.Header.Get("Retry-After"))); werr != nil {
				return fmt.Errorf("%w (after: %w)", werr, apiErr)
			}
			continue
		}
		return apiErr
	}
}

func (c *Client) send(ctx context.Context, method, target string, payload []byte) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json, application/problem+json")
	req.Header.Set("User-Agent", c.userAgent)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

func readAPIError(resp *http.Response, method, path string) *APIError {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	apiErr := &APIError{StatusCode: resp.StatusCode, Method: method, Path: path}
	var p problem
	if json.Unmarshal(raw, &p) == nil && (p.Title != "" || p.Detail != "") {
		apiErr.Title, apiErr.Detail, apiErr.Type = p.Title, p.Detail, p.Type
		apiErr.ErrorCode, apiErr.RequiredPermission = p.ErrorCode, p.RequiredPermission
		return apiErr
	}
	apiErr.Body = strings.TrimSpace(string(raw))
	if apiErr.Body == "" {
		apiErr.Title = http.StatusText(resp.StatusCode)
	}
	return apiErr
}

// Retry policy: 429 is always safe to retry (the server rejected the request
// before acting). 502, 503 and 504 and dropped connections are retried only for
// idempotent methods, because a gateway error on POST may hide a request the
// server already executed, which would create a duplicate. A refused dial means
// nothing was sent, so it is retried for every method.
func retryableStatus(method string, status int) bool {
	switch status {
	case http.StatusTooManyRequests:
		return true
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return idempotent(method)
	}
	return false
}

func retryableTransportError(method string, err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	return idempotent(method)
}

func idempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions:
		return true
	}
	return false
}

func retryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

func (c *Client) wait(ctx context.Context, attempt int, hint time.Duration) error {
	d := c.minBackoff << attempt
	if d > c.maxBackoff || d <= 0 {
		d = c.maxBackoff
	}
	// Full jitter in [d/2, d].
	d = d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
	if hint > d {
		d = min(hint, c.maxBackoff)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func idPath(format string, ids ...int64) string {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return fmt.Sprintf(format, args...)
}
