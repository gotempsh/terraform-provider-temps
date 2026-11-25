// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"net/http"
)

// DNS providers, certificates and email sending.

var dnsPerms = []string{"dns_providers:read", "dns_providers:write"}

// Temps also lists "manual", but its create request has no credentials for
// it, so it cannot be created through the API.
var dnsProviderTypes = []string{"azure", "bunny", "cloudflare", "digitalocean", "gcp", "namecheap", "pebble", "route53"}

var dnsProviderSpec = &gspec{
	Name: "dns_provider",
	Desc: "A DNS host Temps creates records in (certificate challenges, generated hostnames). Hand it zones with " +
		"`temps_dns_provider_domain`.",
	Perms: dnsPerms,
	Fields: []gfield{
		{Name: "name", Kind: kString, Required: true, Desc: "Name of the provider."},
		{Name: "provider_type", Kind: kString, Required: true, CreateOnly: true, OneOf: dnsProviderTypes, Desc: "DNS host."},
		{Name: "credentials", Kind: kJSON, Required: true, Sensitive: true, Read: "-",
			Desc: "Credentials for `provider_type`, for example `jsonencode({ api_token = var.cloudflare_token })` " +
				"for Cloudflare. The `type` key is filled in from `provider_type`."},
		{Name: "description", Kind: kString, Clearable: true, Desc: "What the provider is for."},
		{Name: "is_active", Kind: kBool, Optional: true, Desc: "Whether Temps uses the provider."},
		{Name: "flat_hostnames_supported", Kind: kBool, Computed: true,
			Desc: "Whether the provider can serve generated hostnames one level below the zone."},
		{Name: "last_error", Kind: kString, Computed: true, Volatile: true, Desc: "Last error the provider returned."},
	},
	Create: gop{Method: http.MethodPost, Path: "/dns-providers"},
	Read:   gop{Path: "/dns-providers/{id}"},
	Update: gop{Method: http.MethodPut, Path: "/dns-providers/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/dns-providers/{id}"},
	BodyHook: func(body map[string]any, v gvals, create bool) {
		creds, ok := body["credentials"].(map[string]any)
		if !ok {
			return
		}
		if _, set := creds["type"]; !set {
			pt := scalarString(v["provider_type"])
			creds["type"] = pt
		}
		if create {
			delete(body, "is_active")
		}
	},
}

var dnsProviderDomainSpec = &gspec{
	Name:    "dns_provider_domain",
	Desc:    "A zone a `temps_dns_provider` manages, so Temps creates certificate and hostname records in it.",
	Perms:   dnsPerms,
	Parents: []string{"provider_id"},
	IDKey:   "domain",
	Fields: []gfield{
		{Name: "provider_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the DNS provider."},
		{Name: "domain", Kind: kString, Required: true, CreateOnly: true, Desc: "Zone apex, for example `example.com`."},
		{Name: "auto_manage", Kind: kBool, Optional: true, Desc: "Create records for domains in the zone automatically."},
		{Name: "proxied_by_default", Kind: kBool, Optional: true, Desc: "Proxy new records through the provider (Cloudflare)."},
		{Name: "sync_generated_records", Kind: kBool, Optional: true, Desc: "Create records for Temps-generated hostnames."},
		{Name: "generated_hostname_mode", Kind: kString, Optional: true, Desc: "How generated hostnames are laid out in the zone."},
		{Name: "verified", Kind: kBool, Computed: true, Volatile: true, Desc: "Whether Temps confirmed it can manage the zone."},
		{Name: "zone_id", Kind: kString, Computed: true, Volatile: true, Desc: "Zone ID at the DNS host."},
		{Name: "verification_error", Kind: kString, Computed: true, Volatile: true, Desc: "Why verification failed."},
	},
	Create: gop{Method: http.MethodPost, Path: "/dns-providers/{provider_id}/domains"},
	Read:   gop{Path: "/dns-providers/{provider_id}/domains"},
	List:   true,
	Update: gop{Method: http.MethodPatch, Path: "/dns-providers/{provider_id}/domains/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/dns-providers/{provider_id}/domains/{id}"},
}

var domainSpec = &gspec{
	Name: "domain",
	Desc: "A domain Temps obtains and renews a TLS certificate for. A `dns-01` challenge needs a DNS provider " +
		"managing the zone, or the `dns_challenge_*` record created by hand.",
	Perms: []string{"domains:read", "domains:create", "domains:delete"},
	Fields: []gfield{
		{Name: "domain", Kind: kString, Required: true, Desc: "Domain name, for example `app.example.com` or `*.example.com`."},
		{Name: "challenge_type", Kind: kString, Optional: true, Read: "verification_method",
			OneOf: []string{"http-01", "dns-01"}, Desc: "ACME challenge used to prove control of the domain."},
		{Name: "status", Kind: kString, Computed: true, Volatile: true, Desc: "Certificate status."},
		{Name: "is_wildcard", Kind: kBool, Computed: true, Desc: "Whether the domain is a wildcard."},
		{Name: "dns_challenge_token", Kind: kString, Computed: true, Volatile: true, Desc: "Name of the DNS challenge record."},
		{Name: "dns_challenge_value", Kind: kString, Computed: true, Volatile: true, Desc: "Value of the DNS challenge record."},
		{Name: "last_error", Kind: kString, Computed: true, Volatile: true, Desc: "Last certificate error."},
	},
	Create: gop{Method: http.MethodPost, Path: "/domains"},
	Read:   gop{Path: "/domains/{id}"},
	// Reads take the numeric ID; deletes take the domain name.
	Delete: gop{Method: http.MethodDelete, Path: "/domains/{domain}"},
}

var emailProviderSpec = &gspec{
	Name: "email_provider",
	Desc: "An email service Temps sends through: Amazon SES, Scaleway, or any SMTP relay. Give the credentials for " +
		"`provider_type` in the matching `*_credentials` argument.",
	Perms: []string{"email_providers:read", "email_providers:create", "email_providers:write", "email_providers:delete"},
	Fields: []gfield{
		{Name: "name", Kind: kString, Required: true, Desc: "Name of the provider."},
		{Name: "provider_type", Kind: kString, Required: true, CreateOnly: true, OneOf: []string{"ses", "scaleway", "smtp"},
			Desc: "Email service."},
		{Name: "region", Kind: kString, Required: true, Desc: "Service region, for example `eu-west-1`."},
		{Name: "ses_credentials", Kind: kJSON, Optional: true, Sensitive: true, Read: "-",
			Desc: "SES credentials: `jsonencode({ access_key_id = ..., secret_access_key = ... })`."},
		{Name: "scaleway_credentials", Kind: kJSON, Optional: true, Sensitive: true, Read: "-",
			Desc: "Scaleway credentials: `jsonencode({ api_key = ..., project_id = ... })`."},
		{Name: "smtp_credentials", Kind: kJSON, Optional: true, Sensitive: true, Read: "-",
			Desc: "SMTP relay: `jsonencode({ host = ..., port = 587, username = ..., password = ... })`."},
		{Name: "sns_topic_arn", Kind: kString, Clearable: true, Desc: "SNS topic SES reports bounces and complaints to."},
		{Name: "is_active", Kind: kBool, Optional: true, Desc: "Whether Temps sends through the provider."},
	},
	Create: gop{Method: http.MethodPost, Path: "/email-providers"},
	Read:   gop{Path: "/email-providers/{id}"},
	Update: gop{Method: http.MethodPatch, Path: "/email-providers/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/email-providers/{id}"},
	BodyHook: func(body map[string]any, _ gvals, create bool) {
		if create {
			delete(body, "is_active")
		}
	},
}

var emailDomainSpec = &gspec{
	Name: "email_domain",
	Desc: "A sending domain of a `temps_email_provider`. Publish `dns_records` in the zone, then Temps verifies it; " +
		"authorize projects to send from it with `temps_email_domain_project`.",
	Perms: []string{"email_domains:read", "email_domains:create", "email_domains:delete"},
	IDKey: "domain.id",
	Fields: []gfield{
		{Name: "provider_id", Kind: kInt, Required: true, Read: "domain.provider_id", Desc: "ID of the email provider."},
		{Name: "domain", Kind: kString, Required: true, Read: "domain.domain", Desc: "Domain to send from."},
		{Name: "status", Kind: kString, Computed: true, Volatile: true, Read: "domain.status", Desc: "Verification status."},
		{Name: "verification_error", Kind: kString, Computed: true, Volatile: true, Read: "domain.verification_error",
			Desc: "Why verification failed."},
		{Name: "dns_records", Kind: kJSON, Computed: true, Volatile: true,
			Desc: "Records to publish, as JSON (`jsondecode(...)`): type, name and value of each."},
	},
	Create: gop{Method: http.MethodPost, Path: "/email-domains"},
	Read:   gop{Path: "/email-domains/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/email-domains/{id}"},
}

var emailDomainProjectSpec = &gspec{
	Name:    "email_domain_project",
	Desc:    "Authorizes a project to send email from a `temps_email_domain`.",
	Perms:   []string{"email_domains:read", "email_domains:write"},
	Parents: []string{"email_domain_id"},
	IDAttr:  "project_id",
	Fields: []gfield{
		{Name: "email_domain_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the email domain."},
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Read: "id", Desc: "ID of the project."},
	},
	Create: gop{Method: http.MethodPost, Path: "/email-domains/{email_domain_id}/projects/{project_id}"},
	Read:   gop{Path: "/email-domains/{email_domain_id}/projects"},
	List:   true,
	Delete: gop{Method: http.MethodDelete, Path: "/email-domains/{email_domain_id}/projects/{id}"},
}
