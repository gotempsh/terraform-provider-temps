// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Timestamp is a time Temps returns as RFC 3339 text (servers from
// 2026-11-25 on) or as milliseconds since the epoch (earlier releases).
//
// Either form decodes to the same value: RFC 3339 in UTC at millisecond
// precision, the precision released servers send. A time therefore reads
// back identically whichever server version answered, and switching
// versions changes nothing that is kept in state.
type Timestamp string

// UnmarshalJSON accepts RFC 3339 text, epoch milliseconds or null (which
// leaves the value unchanged), and rejects anything else.
func (t *Timestamp) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if string(b) == "null" {
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		parsed, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return fmt.Errorf("timestamp %q is not RFC 3339 text: %w", s, err)
		}
		*t = newTimestamp(parsed)
		return nil
	}
	ms, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return fmt.Errorf("timestamp %s is neither RFC 3339 text nor epoch milliseconds", b)
	}
	*t = newTimestamp(time.UnixMilli(ms))
	return nil
}

// newTimestamp is the normalised form of a time.
func newTimestamp(at time.Time) Timestamp {
	return Timestamp(at.UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano))
}
