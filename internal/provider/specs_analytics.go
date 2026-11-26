// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import "net/http"

// Product analytics conversion goals, traffic filters and settings.

var analyticsPerms = []string{"analytics:read", "analytics:write"}

var analyticsGoalSpec = &gspec{
	Name: "analytics_goal",
	Desc: "A conversion goal of a project's analytics: a custom event (`event`) or a page view (`page`) that " +
		"counts as a conversion, optionally carrying revenue.",
	Perms:   analyticsPerms,
	Parents: []string{"project_id"},
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "name", Kind: kString, Required: true, Desc: "Name of the goal, unique in the project."},
		{Name: "kind", Kind: kString, Required: true, OneOf: []string{"event", "page"}, Desc: "What counts as a conversion."},
		{Name: "event_name", Kind: kString, Clearable: true, Desc: "Custom event that converts (`event`)."},
		{Name: "event_filter", Kind: kJSON, Optional: true, Default: "[]", Subset: true,
			Desc: "Conditions a conversion must meet, as in funnel steps: " +
				"`jsonencode([{ type = \"custom_data\", value = { path = \"plan\", value = \"pro\" } }])`. " +
				"Temps applies them to page goals too. The default, `[]`, has none."},
		{Name: "page_path", Kind: kString, Clearable: true, Desc: "Page path that converts (`page`), for example `/thank-you`."},
		{Name: "revenue_property", Kind: kString, Clearable: true, Desc: "Event property holding the conversion's revenue."},
		{Name: "revenue_currency", Kind: kString, Clearable: true, Desc: "ISO 4217 currency of the revenue, in capitals (`EUR`)."},
	},
	Create: gop{Method: http.MethodPost, Path: "/projects/{project_id}/analytics/goals"},
	Read:   gop{Path: "/projects/{project_id}/analytics/goals/{id}"},
	Update: gop{Method: http.MethodPut, Path: "/projects/{project_id}/analytics/goals/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/projects/{project_id}/analytics/goals/{id}"},
}

var analyticsTrafficFilterSpec = &gspec{
	Name: "analytics_traffic_filter",
	Desc: "Excludes visits from a project's analytics: those from a referrer domain (and its subdomains), an IP " +
		"address or range, a network (ASN) or a country. Changing it replaces the filter.",
	Perms:   analyticsPerms,
	Parents: []string{"project_id"},
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "kind", Kind: kString, Required: true, OneOf: []string{"referrer", "ip", "asn", "country"}, Desc: "What the filter matches."},
		{Name: "value", Kind: kString, Required: true, Read: "-",
			Desc: "A domain or URL (`referrer`), an IP address or CIDR range (`ip`), a network number with or without " +
				"`AS` (`asn`), or a two-letter country code (`country`). Temps stores it normalized, in `normalized_value`."},
		{Name: "note", Kind: kString, Optional: true, Desc: "Why the filter exists."},
		{Name: "normalized_value", Read: "value", Kind: kString, Computed: true,
			Desc: "The value as Temps matches it, for example `spam.example` for `https://www.spam.example/x`."},
	},
	Create: gop{Method: http.MethodPost, Path: "/projects/{project_id}/analytics/traffic-filters"},
	Read:   gop{Path: "/projects/{project_id}/analytics/traffic-filters"},
	List:   true, ListKey: "filters", Paged: true,
	Delete: gop{Method: http.MethodDelete, Path: "/projects/{project_id}/analytics/traffic-filters/{id}"},
}

var projectAnalyticsSettingsSpec = &gspec{
	Name:       "project_analytics_settings",
	Desc:       "Which kinds of unwanted traffic a project's analytics leaves out of every report.",
	Perms:      analyticsPerms,
	Parents:    []string{"project_id"},
	Singleton:  true,
	DeleteNote: "Destroying the resource leaves the settings as they are.",
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "exclude_datacenter_traffic", Kind: kBool, Optional: true,
			Desc: "Leave out visits from hosting and cloud networks. Needs an ASN database on the instance (`datacenter_detection_available`)."},
		{Name: "exclude_spam_referrers", Kind: kBool, Optional: true,
			Desc: "Leave out visits whose referrer is on Temps' referrer-spam list."},
		{Name: "datacenter_detection_available", Read: "datacenter_detection.available", Kind: kBool, Computed: true, Volatile: true,
			Desc: "Whether the instance can tell datacenter traffic apart."},
	},
	Create: gop{Method: http.MethodPatch, Path: "/projects/{project_id}/analytics/settings"},
	Read:   gop{Path: "/projects/{project_id}/analytics/settings"},
	Update: gop{Method: http.MethodPatch, Path: "/projects/{project_id}/analytics/settings"},
}
