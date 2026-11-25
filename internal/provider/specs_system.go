// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"net/http"
)

// Built-in subsystems: KV, Blob, the preview gateway and notification
// preferences.

func disabled(obj map[string]any) bool { return obj["enabled"] == false }

func managedSubsystem(name, title, path string, perms []string, extra []gfield) *gspec {
	fields := append([]gfield{
		{Name: "docker_image", Kind: kString, Optional: true, Desc: "Image to run; Temps picks its default when unset. Changing it upgrades in place."},
	}, extra...)
	fields = append(fields,
		gfield{Name: "healthy", Kind: kBool, Computed: true, Volatile: true, Desc: "Whether the service answers health checks."},
		gfield{Name: "version", Kind: kString, Computed: true, Volatile: true, Desc: "Version of the running service."},
	)
	return &gspec{
		Name: name,
		Desc: "Turns on Temps' built-in " + title + " service, which Temps runs as a container and offers to every project. " +
			"Destroying the resource turns it off: Temps stops the container but keeps it and its data, so " +
			"enabling the service again resumes it. Name its container with `temps_" + name + "_settings` first if " +
			"another Temps instance shares the Docker host.",
		Perms:     perms,
		Singleton: true,
		Fields:    fields,
		Create:    gop{Method: http.MethodPost, Path: path + "/enable", Envelope: "status"},
		Read:      gop{Path: path + "/status"},
		Update:    gop{Method: http.MethodPatch, Path: path + "/update", Envelope: "status"},
		Delete:    gop{Method: http.MethodDelete, Path: path + "/disable"},
		Gone:      disabled,
	}
}

var kvSpec = managedSubsystem("kv", "key-value (Redis-compatible)", "/kv", []string{"kv:read", "kv:write", "kv:delete"}, []gfield{
	{Name: "max_memory", Kind: kString, Optional: true, CreateOnly: true, Read: "-", Desc: "Memory limit, for example `256mb`."},
	{Name: "persistence", Kind: kBool, Optional: true, Default: false, CreateOnly: true, Read: "-", Desc: "Persist data to disk."},
})

var blobSpec = managedSubsystem("blob", "object storage (S3-compatible)", "/blob", []string{"blob:read", "blob:write", "blob:delete"}, []gfield{
	{Name: "root_user", Kind: kString, Optional: true, CreateOnly: true, Read: "-", Desc: "Administrator user of the store."},
	{Name: "root_password", Kind: kString, Optional: true, CreateOnly: true, Sensitive: true, Read: "-", Desc: "Administrator password of the store."},
})

func containerNameSpec(name, title, path string, perms []string) *gspec {
	return &gspec{
		Name: name + "_settings",
		Desc: "Name of the container Temps runs the built-in " + title + " service in. Temps refuses a change while " +
			"the container is in use; set it before `temps_" + name + "`.",
		Perms:     perms,
		Singleton: true,
		Fields: []gfield{
			{Name: "container_name", Kind: kString, Required: true, Desc: "Container name; it must start with `required_prefix`."},
			{Name: "required_prefix", Kind: kString, Computed: true, Desc: "Prefix every name must have."},
			{Name: "default_container_name", Kind: kString, Computed: true, Desc: "Name used when none is set."},
			{Name: "in_use", Kind: kBool, Computed: true, Volatile: true, Desc: "Whether the container exists now."},
		},
		Create:     gop{Method: http.MethodPatch, Path: path + "/settings"},
		Read:       gop{Path: path + "/settings"},
		Update:     gop{Method: http.MethodPatch, Path: path + "/settings"},
		DeleteNote: "Destroying the resource leaves the name as it is; it only stops managing it.",
	}
}

var kvSettingsSpec = containerNameSpec("kv", "key-value", "/kv", []string{"kv:read", "kv:write", "system:admin"})
var blobSettingsSpec = containerNameSpec("blob", "object storage", "/blob", []string{"blob:read", "blob:write", "system:admin"})

var previewGatewaySpec = &gspec{
	Name:      "preview_gateway_settings",
	Desc:      "The gateway container that serves previews of AI sandboxes and branches.",
	Perms:     []string{"settings:read", "settings:write"},
	Singleton: true,
	Fields: []gfield{
		{Name: "enabled", Kind: kBool, Optional: true, Desc: "Run the gateway."},
		{Name: "image", Kind: kString, Optional: true, Desc: "Gateway image."},
		{Name: "container_name", Kind: kString, Optional: true, Desc: "Name of the gateway container."},
		{Name: "host_port", Kind: kInt, Optional: true, Desc: "Host port the gateway listens on."},
		{Name: "auto_upgrade", Kind: kBool, Optional: true, Desc: "Upgrade the gateway with Temps."},
	},
	Create:     gop{Method: http.MethodPatch, Path: "/preview-gateway/settings"},
	Read:       gop{Path: "/preview-gateway/settings"},
	Update:     gop{Method: http.MethodPatch, Path: "/preview-gateway/settings"},
	DeleteNote: "Destroying the resource leaves the settings as they are; it only stops managing them.",
}

func notificationPrefBool(name, desc string) gfield {
	return gfield{Name: name, Kind: kBool, Optional: true, Desc: desc}
}

var notificationPreferencesSpec = &gspec{
	Name: "notification_preferences",
	Desc: "Which events Temps notifies about and how. Unset arguments keep their current value. Destroying the " +
		"resource restores the defaults.",
	Perms:     []string{"notification_preferences:read", "notification_preferences:write"},
	Singleton: true,
	Fields: []gfield{
		notificationPrefBool("email_enabled", "Send notifications by email."),
		notificationPrefBool("slack_enabled", "Send notifications to Slack."),
		notificationPrefBool("deployment_failures_enabled", "Notify about failed deployments."),
		notificationPrefBool("build_errors_enabled", "Notify about build errors."),
		notificationPrefBool("runtime_errors_enabled", "Notify about runtime errors."),
		notificationPrefBool("backup_failures_enabled", "Notify about failed backups."),
		notificationPrefBool("backup_successes_enabled", "Notify about successful backups."),
		notificationPrefBool("ssl_expiration_enabled", "Notify before certificates expire."),
		notificationPrefBool("domain_expiration_enabled", "Notify before domains expire."),
		notificationPrefBool("dns_changes_enabled", "Notify about DNS changes."),
		notificationPrefBool("route_downtime_enabled", "Notify when a route goes down."),
		notificationPrefBool("load_balancer_issues_enabled", "Notify about load balancer problems."),
		notificationPrefBool("s3_connection_issues_enabled", "Notify about S3 connection problems."),
		notificationPrefBool("retention_policy_violations_enabled", "Notify about retention policy violations."),
		notificationPrefBool("batch_similar_notifications", "Group similar notifications."),
		notificationPrefBool("weekly_digest_enabled", "Send a weekly digest."),
		{Name: "minimum_severity", Kind: kString, Optional: true, Desc: "Least severity notified, for example `warning`."},
		{Name: "error_threshold", Kind: kInt, Optional: true, Desc: "Errors within `error_time_window` that trigger a notification."},
		{Name: "error_time_window", Kind: kInt, Optional: true, Desc: "Window for `error_threshold`, in minutes."},
		{Name: "ssl_days_before_expiration", Kind: kInt, Optional: true, Desc: "Days before expiry to warn about certificates."},
		{Name: "digest_send_day", Kind: kString, Optional: true, Desc: "Day the weekly digest is sent, for example `monday`."},
		{Name: "digest_send_time", Kind: kString, Optional: true, Desc: "Time the weekly digest is sent, `HH:MM`."},
	},
	Create:       gop{Method: http.MethodPut, Path: "/notification-preferences"},
	Read:         gop{Path: "/notification-preferences"},
	Update:       gop{Method: http.MethodPut, Path: "/notification-preferences"},
	Delete:       gop{Method: http.MethodDelete, Path: "/notification-preferences"},
	MergeCurrent: true,
	BodyEnvelope: "preferences",
}
