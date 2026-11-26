// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Released servers send deployment times as epoch milliseconds, newer ones
// as RFC 3339 text; both read back as RFC 3339.
func TestGetDeploymentDecodesBothTimestampForms(t *testing.T) {
	for _, tc := range []struct{ name, body, created, finished string }{
		{"epoch milliseconds", `{"id":5,"status":"completed","created_at":1767225600000,"finished_at":1767225660500}`,
			"2026-01-01T00:00:00Z", "2026-01-01T00:01:00.5Z"},
		{"RFC 3339", `{"id":5,"status":"completed","created_at":"2026-01-01T00:00:00Z","finished_at":"2026-01-01T00:01:00Z"}`,
			"2026-01-01T00:00:00Z", "2026-01-01T00:01:00Z"},
		{"not finished", `{"id":5,"status":"running","created_at":1767225600000,"finished_at":null}`,
			"2026-01-01T00:00:00Z", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/projects/1/deployments/5" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c, err := New(Config{Endpoint: srv.URL, APIKey: "tk_test"})
			if err != nil {
				t.Fatal(err)
			}
			d, err := c.GetDeployment(context.Background(), 1, 5)
			if err != nil {
				t.Fatalf("GetDeployment: %v", err)
			}
			if string(d.CreatedAt) != tc.created {
				t.Errorf("created_at = %q, want %q", d.CreatedAt, tc.created)
			}
			switch {
			case tc.finished == "" && d.FinishedAt != nil:
				t.Errorf("finished_at = %q, want null", *d.FinishedAt)
			case tc.finished != "" && (d.FinishedAt == nil || string(*d.FinishedAt) != tc.finished):
				t.Errorf("finished_at = %v, want %q", d.FinishedAt, tc.finished)
			}
		})
	}
}

func TestTimestampRejectsOtherJSON(t *testing.T) {
	var ts Timestamp
	if err := ts.UnmarshalJSON([]byte(`true`)); err == nil {
		t.Fatalf("decoded true as %q, want an error", ts)
	}
}
