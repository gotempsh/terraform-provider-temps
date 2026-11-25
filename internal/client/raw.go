// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Raw sends a JSON request and returns the decoded JSON response, or nil when
// the server answers without a body. Numbers decode as json.Number so 64-bit
// IDs survive. It shares retries and error decoding with the typed methods.
func (c *Client) Raw(ctx context.Context, method, path string, query url.Values, in any) (any, error) {
	var raw json.RawMessage
	if err := c.do(ctx, method, path, query, in, &raw); err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%s %s: decoding response: %w", method, path, err)
	}
	return v, nil
}

// PathSegment escapes one value for use as a path segment (a domain, a slug,
// a secret name).
func PathSegment(s string) string {
	return strings.ReplaceAll(url.PathEscape(s), "+", "%2B")
}
