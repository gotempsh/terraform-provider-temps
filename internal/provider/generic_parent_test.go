// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestGenericUnsetOptionalParentLeftOutOfID checks that an optional parent
// left out of the configuration, still unknown when the object is created,
// is not written into the ID (a platform Shield entry is "5", not "0/5").
func TestGenericUnsetOptionalParentLeftOutOfID(t *testing.T) {
	r := &genericResource{s: shieldEntrySpec}
	if got := r.tfID(gvals{"project_id": types.Int64Unknown()}, "5"); got != "5" {
		t.Fatalf("ID with an unknown project = %q, want 5", got)
	}
	if got := r.tfID(gvals{"project_id": types.Int64Value(8)}, "5"); got != "8/5" {
		t.Fatalf("ID with a project = %q, want 8/5", got)
	}
	for _, v := range []gvals{
		{"id": types.StringValue("5"), "project_id": types.Int64Null()},
		{"id": types.StringValue("8/5"), "project_id": types.Int64Value(8)},
	} {
		if got := r.serverID(v); got != "5" {
			t.Fatalf("serverID(%v) = %q, want 5", v, got)
		}
	}
}
