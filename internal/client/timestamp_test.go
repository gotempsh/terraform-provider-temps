// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTimestampNormalisesBothForms(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`1767225600000`, "2026-01-01T00:00:00Z"},
		{`1767225660500`, "2026-01-01T00:01:00.5Z"},
		{`"2026-01-01T00:00:00Z"`, "2026-01-01T00:00:00Z"},
		{`"2026-01-01T00:01:00.500Z"`, "2026-01-01T00:01:00.5Z"},
		// Newer servers keep the database's microseconds; released ones
		// sent milliseconds. Both read the same.
		{`"2026-01-01T00:01:00.500123Z"`, "2026-01-01T00:01:00.5Z"},
		{`"2026-01-01T01:00:00+01:00"`, "2026-01-01T00:00:00Z"},
	} {
		var ts Timestamp
		if err := json.Unmarshal([]byte(tc.in), &ts); err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if string(ts) != tc.want {
			t.Errorf("%s decoded as %q, want %q", tc.in, ts, tc.want)
		}
	}
}

func TestTimestampRejectsOtherJSON(t *testing.T) {
	for _, in := range []string{`true`, `"tomorrow"`, `"2026-01-01"`, `1.5e12`, `{}`, `[]`, `""`} {
		var ts Timestamp
		if err := json.Unmarshal([]byte(in), &ts); err == nil {
			t.Errorf("decoded %s as %q, want an error", in, ts)
		}
	}
}

// Released servers send next_run and last_run as epoch milliseconds, newer
// ones as RFC 3339 text; both read back as the same RFC 3339 time.
func TestGetBackupScheduleDecodesBothRunTimeForms(t *testing.T) {
	const want = "2026-01-02T03:00:00Z"
	for _, tc := range []struct{ name, body, next, last string }{
		{"epoch milliseconds", `{"id":4,"next_run":1767322800000,"last_run":1767236400000}`, want, "2026-01-01T03:00:00Z"},
		{"RFC 3339", `{"id":4,"next_run":"2026-01-02T03:00:00Z","last_run":"2026-01-01T03:00:00.000000Z"}`, want, "2026-01-01T03:00:00Z"},
		{"never run", `{"id":4,"next_run":"2026-01-02T03:00:00Z","last_run":null}`, want, ""},
		{"disabled", `{"id":4,"next_run":null,"last_run":null}`, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := scheduleServer(t, tc.body)
			s, err := c.GetBackupSchedule(context.Background(), 4)
			if err != nil {
				t.Fatalf("GetBackupSchedule: %v", err)
			}
			if got := tsString(s.NextRun); got != tc.next {
				t.Errorf("next_run = %q, want %q", got, tc.next)
			}
			if got := tsString(s.LastRun); got != tc.last {
				t.Errorf("last_run = %q, want %q", got, tc.last)
			}
		})
	}
}

func TestGetBackupScheduleRejectsOtherRunTimes(t *testing.T) {
	for _, body := range []string{`{"id":4,"next_run":"soon"}`, `{"id":4,"next_run":{"at":1}}`, `{"id":4,"last_run":true}`} {
		c := scheduleServer(t, body)
		if s, err := c.GetBackupSchedule(context.Background(), 4); err == nil {
			t.Errorf("%s decoded as next_run %q, last_run %q; want an error", body, tsString(s.NextRun), tsString(s.LastRun))
		}
	}
}

func scheduleServer(t *testing.T, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/backups/schedules/4" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{Endpoint: srv.URL, APIKey: "tk_test"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func tsString(t *Timestamp) string {
	if t == nil {
		return ""
	}
	return string(*t)
}
