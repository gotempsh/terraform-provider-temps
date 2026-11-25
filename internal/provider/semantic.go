// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// jsonEqual reports whether two JSON documents are equal ignoring key order,
// whitespace and number formatting (the server re-encodes values: 1.50
// comes back as 1.5 and object keys are sorted).
func jsonEqual(a, b string) bool {
	var va, vb any
	if json.Unmarshal([]byte(a), &va) != nil || json.Unmarshal([]byte(b), &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// jsonValue keeps the configured spelling of a JSON value when the server's
// is semantically equal, so re-encoding never shows as drift.
func jsonValue(prior types.String, server []byte) types.String {
	server = bytes.TrimSpace(server)
	if len(server) == 0 || string(server) == "null" {
		return types.StringNull()
	}
	if known(prior) && jsonEqual(prior.ValueString(), string(server)) {
		return prior
	}
	return types.StringValue(string(server))
}

// timeValue keeps the configured spelling of a timestamp when the server's
// names the same instant (the server rewrites offsets to UTC).
func timeValue(prior types.String, server *string) types.String {
	if server == nil {
		return types.StringNull()
	}
	if known(prior) {
		a, errA := time.Parse(time.RFC3339, prior.ValueString())
		b, errB := time.Parse(time.RFC3339, *server)
		if errA == nil && errB == nil && a.Equal(b) {
			return prior
		}
	}
	return types.StringValue(*server)
}

// jsonString validates that a string attribute holds one JSON document.
type jsonString struct{}

func (jsonString) Description(context.Context) string {
	return "must be a JSON document, for example from jsonencode()"
}
func (v jsonString) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (jsonString) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !known(req.ConfigValue) {
		return
	}
	if !json.Valid([]byte(req.ConfigValue.ValueString())) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid JSON",
			fmt.Sprintf("%s must be a JSON document; wrap values in jsonencode(), for example jsonencode(true). Got %s.", req.Path, req.ConfigValue))
	}
}

// rfc3339 validates an RFC 3339 timestamp.
type rfc3339 struct{}

func (rfc3339) Description(context.Context) string {
	return "must be an RFC 3339 timestamp such as 2030-01-01T00:00:00Z"
}
func (v rfc3339) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (rfc3339) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !known(req.ConfigValue) {
		return
	}
	if _, err := time.Parse(time.RFC3339, req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid timestamp",
			fmt.Sprintf("%s must be an RFC 3339 timestamp such as 2030-01-01T00:00:00Z: %v", req.Path, err))
	}
}

// canonicalCIDR requires the exact form the server stores, so the policy
// never shows a diff after apply: a network address with a prefix length,
// lower-case IPv6, and no IPv4-mapped IPv6 addresses.
type canonicalCIDR struct{}

func (canonicalCIDR) Description(context.Context) string {
	return "must be a canonical CIDR such as 10.0.0.0/8, 203.0.113.7/32 or 2001:db8::/32"
}
func (v canonicalCIDR) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (canonicalCIDR) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !known(req.ConfigValue) {
		return
	}
	s := req.ConfigValue.ValueString()
	want, problem := canonicalizeCIDR(s)
	if problem == "" && want != s {
		problem = "Temps stores it as " + want + "; write it that way to avoid a permanent diff"
	}
	if problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid CIDR", fmt.Sprintf("CIDR %q: %s.", s, problem))
	}
}

// canonicalizeCIDR returns the server's spelling of a CIDR or bare address,
// or a reason it is invalid.
func canonicalizeCIDR(s string) (string, string) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		a, aerr := netip.ParseAddr(s)
		if aerr != nil {
			return "", "not an IPv4/IPv6 CIDR or address"
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	if p.Addr().Zone() != "" {
		return "", "zones are not allowed"
	}
	if p.Addr().Is4In6() {
		a := p.Addr().Unmap()
		bits := p.Bits() - 96
		if bits < 0 {
			return "", "an IPv4-mapped IPv6 prefix shorter than /96 has no IPv4 equivalent"
		}
		p = netip.PrefixFrom(a, bits)
	}
	if p.Masked() != p {
		return "", "host bits are set; use the network address " + p.Masked().String()
	}
	return p.String(), ""
}

var (
	flagKeyRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	cronSlugRE   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	teamSlugRE   = regexp.MustCompile(`^[a-z0-9-]+$`)
	hostnameRE   = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	redirectCode = []int64{301, 302, 303, 307, 308}
)

// notBlank rejects a string of only whitespace, which Temps treats as empty.
type notBlank struct{}

func (notBlank) Description(context.Context) string               { return "must contain a non-space character" }
func (v notBlank) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (notBlank) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if known(req.ConfigValue) && strings.TrimSpace(req.ConfigValue.ValueString()) == "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Blank value", fmt.Sprintf("%s must contain a non-space character.", req.Path))
	}
}

// joinTicks renders values as "a`, `b" for use inside a pair of backticks.
func joinTicks(vals []string) string { return strings.Join(vals, "`, `") }
