// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

// The response shapes Temps changed on 2026-11-25: growing lists became
// pages, and times became RFC 3339 text. The fakes answer in the new shapes
// unless told to answer like released servers up to v0.1.0-beta.63.

// fakeNextRun is when every fake backup schedule runs next.
const fakeNextRun = "2026-01-02T03:00:00Z"

// writePage answers a list request with items, which are newest first: one
// page of them, sized like Temps does (default 20, at most 100), or the
// whole list as a bare array when legacy.
func writePage[T any](w http.ResponseWriter, r *http.Request, items []T, legacy bool) {
	if items == nil {
		items = []T{}
	}
	if legacy {
		writeJSON(w, http.StatusOK, items)
		return
	}
	page, size := queryInt(r, "page", 1), queryInt(r, "page_size", 20)
	page, size = max(page, 1), min(max(size, 1), 100)
	start := min((page-1)*size, len(items))
	end := min(start+size, len(items))
	writeJSON(w, http.StatusOK, map[string]any{"items": items[start:end], "total": len(items), "page": page, "page_size": size})
}

func queryInt(r *http.Request, name string, def int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return def
	}
	return n
}

// scheduleJSON is a backup schedule as the fake's server version sends it.
func (f *fakeTemps) scheduleJSON(s client.BackupSchedule) any {
	if !f.legacyShapes {
		return s
	}
	b, _ := json.Marshal(s)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	for _, k := range []string{"next_run", "last_run"} {
		if t, ok := out[k].(string); ok {
			out[k] = epochMillis(client.Timestamp(t))
		}
	}
	return out
}
