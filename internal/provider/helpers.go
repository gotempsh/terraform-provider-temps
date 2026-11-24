// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

// clientFromResource extracts the configured client in a resource's Configure.
func configureClient(data any, diags *diag.Diagnostics) *client.Client {
	if data == nil {
		return nil // provider not configured yet (validation phase)
	}
	c, ok := data.(*client.Client)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T", data))
		return nil
	}
	return c
}

type resourceBase struct {
	client *client.Client
}

func (r *resourceBase) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if c := configureClient(req.ProviderData, &resp.Diagnostics); c != nil {
		r.client = c
	}
}

type dataSourceBase struct {
	client *client.Client
}

func (d *dataSourceBase) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if c := configureClient(req.ProviderData, &resp.Diagnostics); c != nil {
		d.client = c
	}
}

// addAPIError turns a client error into a diagnostic that keeps the problem
// title and detail, names the object, and on 403 lists the permissions the
// resource needs.
func addAPIError(diags *diag.Diagnostics, summary string, err error, permissions []string) {
	detail := err.Error()
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden && len(permissions) > 0 {
		detail += "\n\nThe API key needs these permissions for this resource: " + strings.Join(permissions, ", ") + "."
	}
	if client.IsUnsupported(err) {
		detail += "\n\nThe Temps server does not provide this endpoint. It is probably older than the feature; upgrade Temps or remove the argument."
	}
	diags.AddError(summary, detail)
}

func formatID(id int64) string { return strconv.FormatInt(id, 10) }

func parseID(s string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%q is not a positive integer ID", s)
	}
	return id, nil
}

// parseCompositeID parses "a/b" into two positive IDs.
func parseCompositeID(s, format string) (int64, int64, error) {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("import ID %q must have the form %s", s, format)
	}
	a, err := parseID(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("import ID %q must have the form %s: %w", s, format, err)
	}
	b, err := parseID(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("import ID %q must have the form %s: %w", s, format, err)
	}
	return a, b, nil
}

// known reports whether a value is set in configuration or plan.
func known(v attr.Value) bool { return !v.IsNull() && !v.IsUnknown() }

func strPtr(v types.String) *string {
	if !known(v) {
		return nil
	}
	s := v.ValueString()
	return &s
}

func int64Ptr(v types.Int64) *int64 {
	if !known(v) {
		return nil
	}
	i := v.ValueInt64()
	return &i
}

func boolPtr(v types.Bool) *bool {
	if !known(v) {
		return nil
	}
	b := v.ValueBool()
	return &b
}

func ptr[T any](v T) *T { return &v }

func int64SetValue(ids []int64) types.Set {
	vals := make([]attr.Value, len(ids))
	for i, id := range ids {
		vals[i] = types.Int64Value(id)
	}
	return types.SetValueMust(types.Int64Type, vals)
}

func int64sFromSet(ctx context.Context, s types.Set, diags *diag.Diagnostics) []int64 {
	if !known(s) {
		return nil
	}
	var out []int64
	diags.Append(s.ElementsAs(ctx, &out, false)...)
	return out
}

func stringsFromList(ctx context.Context, l types.List, diags *diag.Diagnostics) []string {
	if !known(l) {
		return nil
	}
	var out []string
	diags.Append(l.ElementsAs(ctx, &out, false)...)
	return out
}

func stringListValue(vals []string) types.List {
	elems := make([]attr.Value, len(vals))
	for i, v := range vals {
		elems[i] = types.StringValue(v)
	}
	return types.ListValueMust(types.StringType, elems)
}

// waitFor polls check every interval until it reports done, returns an error,
// or ctx ends. On timeout the returned error names what was awaited and the
// last observed state.
func waitFor(ctx context.Context, what string, interval time.Duration, check func() (done bool, state string, err error)) error {
	last := "unknown"
	for {
		done, state, err := check()
		if state != "" {
			last = state
		}
		if err != nil {
			// The deadline may expire during the poll request itself: report
			// it as the timeout it is, not as a bare transport error.
			if ctx.Err() != nil {
				return waitStopped(ctx, what, last)
			}
			return err
		}
		if done {
			return nil
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return waitStopped(ctx, what, last)
		case <-t.C:
		}
	}
}

func waitStopped(ctx context.Context, what, last string) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("timed out waiting for %s; last status %q (raise the matching timeouts value if the server is slow)", what, last)
	}
	return fmt.Errorf("stopped waiting for %s (last status %q): %w", what, last, ctx.Err())
}

// withHint appends advice for the user to an error's diagnostic text.
type withHint struct {
	err  error
	hint string
}

func (e withHint) Error() string { return e.err.Error() + "\n\n" + e.hint }
func (e withHint) Unwrap() error { return e.err }

// pollInterval is how often async waits poll; tests shorten it.
var pollInterval = 3 * time.Second

func asAPIError(err error, target **client.APIError) bool { return errors.As(err, target) }

func stringSetValue(vals []string) types.Set {
	elems := make([]attr.Value, len(vals))
	for i, v := range vals {
		elems[i] = types.StringValue(v)
	}
	return types.SetValueMust(types.StringType, elems)
}

func stringsFromSet(ctx context.Context, s types.Set, diags *diag.Diagnostics) []string {
	if !known(s) {
		return nil
	}
	out := []string{}
	diags.Append(s.ElementsAs(ctx, &out, false)...)
	return out
}

// objectAsOptions reads a possibly null or unknown object into a struct.
var objectAsOptions = basetypes.ObjectAsOptions{UnhandledNullAsEmpty: true, UnhandledUnknownAsEmpty: true}
