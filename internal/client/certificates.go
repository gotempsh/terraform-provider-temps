// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"net/http"
)

// DomainCertificateSettings is the renewal and expiry-alert policy of a TLS
// domain (GET and PUT /domains/{id}/certificate-settings). PUT replaces the
// whole document: AlertDays and AlertsEnabled are always sent, and nil
// inherits the instance setting.
type DomainCertificateSettings struct {
	// ChallengeType is the ACME challenge future orders use.
	ChallengeType string `json:"challenge_type"`
	// RenewalDays starts ACME renewal this many days before expiry (1–90).
	RenewalDays   int64  `json:"renewal_days"`
	AlertDays     *int64 `json:"alert_days"`
	AlertsEnabled *bool  `json:"alerts_enabled"`
	// RenewalMode is returned, never sent: manual for an imported
	// certificate, acme otherwise.
	RenewalMode string `json:"renewal_mode,omitempty"`
}

func certificateSettingsPath(domainID int64) string {
	return idPath("/domains/%d/certificate-settings", domainID)
}

// GetDomainCertificateSettings reads a domain's certificate settings; a
// domain that never had them saved returns the defaults.
func (c *Client) GetDomainCertificateSettings(ctx context.Context, domainID int64) (*DomainCertificateSettings, error) {
	var out DomainCertificateSettings
	return &out, c.do(ctx, http.MethodGet, certificateSettingsPath(domainID), nil, nil, &out)
}

// SetDomainCertificateSettings replaces a domain's certificate settings.
func (c *Client) SetDomainCertificateSettings(ctx context.Context, domainID int64, s DomainCertificateSettings) (*DomainCertificateSettings, error) {
	s.RenewalMode = ""
	var out DomainCertificateSettings
	return &out, c.do(ctx, http.MethodPut, certificateSettingsPath(domainID), nil, s, &out)
}
