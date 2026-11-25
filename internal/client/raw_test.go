// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestRawSkipsPlainTextSuccessBody(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "Role assigned successfully")
	}))
	got, err := c.Raw(context.Background(), http.MethodPost, "/users/1/roles", nil, map[string]any{"role_type": "user"})
	if err != nil || got != nil {
		t.Fatalf("Raw = %v, %v; want nil, nil", got, err)
	}
}

func TestRawDecodesJSONSentAsText(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, `{"id": 9007199254740993}`)
	}))
	got, err := c.Raw(context.Background(), http.MethodGet, "/things/1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// UseNumber keeps large IDs exact.
	if n, ok := got.(map[string]any)["id"].(json.Number); !ok || n.String() != "9007199254740993" {
		t.Fatalf("Raw = %#v", got)
	}
}

func TestPathSegmentEscapes(t *testing.T) {
	if got := PathSegment("a b/c?d"); got != "a%20b%2Fc%3Fd" {
		t.Fatalf("PathSegment = %q", got)
	}
}
