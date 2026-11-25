// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

// Access vaults, ingress and load balancing, network policy, traffic
// delivery, cluster networking, nodes and workload volumes.

var vaultPerms = []string{"settings:read", "settings:write"}

var accessServiceSpec = &gspec{
	Name: "access_service",
	Desc: "A service sandboxed agents may call through the Temps access broker: the exact origin, paths and methods " +
		"they may use, and the credential the broker injects so the agent never sees it.",
	Perms: vaultPerms,
	Fields: []gfield{
		{Name: "name", Kind: kString, Required: true, Desc: "Name of the service."},
		{Name: "enabled", Kind: kBool, Required: true, Desc: "Let the broker grant access to it."},
		{Name: "max_ttl_seconds", Kind: kInt, Required: true, Desc: "Longest an access session may last."},
		{Name: "policy", Kind: kJSON, Required: true, Subset: true,
			Desc: "Destination and request scope, for example " +
				"`jsonencode({ origin = \"https://api.example.com\", methods = [\"GET\"], path_prefixes = [\"/v1\"] })`."},
		{Name: "credential", Kind: kString, Optional: true, Sensitive: true, Read: "-", Desc: "Credential the broker injects."},
		{Name: "vault_id", Kind: kString, Optional: true, Read: "-", Desc: "ID of a `temps_access_vault` to file the service in."},
		{Name: "has_credential", Kind: kBool, Computed: true, Volatile: true, Desc: "Whether a credential is stored."},
		{Name: "version", Kind: kInt, Computed: true, Volatile: true, Desc: "Revision, raised by every change."},
	},
	Create:          gop{Method: http.MethodPost, Path: "/access-services"},
	Read:            gop{Path: "/access-services/{id}"},
	Update:          gop{Method: http.MethodPut, Path: "/access-services/{id}"},
	ExpectedVersion: "expected_version",
	Delete:          gop{Method: http.MethodDelete, Path: "/access-services/{id}"},
}

var accessVaultSpec = &gspec{
	Name:  "access_vault",
	Desc:  "A named group of `temps_access_service`s that sandboxes are given access to together.",
	Perms: vaultPerms,
	Fields: []gfield{
		{Name: "name", Kind: kString, Required: true, Desc: "Name of the vault."},
		{Name: "service_ids", Kind: kStringSet, Required: true, Desc: "IDs of the access services in the vault."},
		{Name: "version", Kind: kInt, Computed: true, Volatile: true, Desc: "Revision, raised by every change."},
	},
	Create:          gop{Method: http.MethodPost, Path: "/access-vaults", Envelope: "."},
	Read:            gop{Path: "/access-vaults/{id}", Envelope: "vault"},
	Update:          gop{Method: http.MethodPut, Path: "/access-vaults/{id}", Envelope: "."},
	ExpectedVersion: "expected_version",
	Delete:          gop{Method: http.MethodDelete, Path: "/access-vaults/{id}"},
}

var accessBundleSpec = &gspec{
	Name:    "access_bundle",
	Desc:    "A subset of a vault's services granted together for a limited time.",
	Perms:   vaultPerms,
	Parents: []string{"vault_id"},
	Fields: []gfield{
		{Name: "vault_id", Kind: kString, Required: true, Path: true, Desc: "ID of the access vault."},
		{Name: "name", Kind: kString, Required: true, Desc: "Name of the bundle."},
		{Name: "service_ids", Kind: kStringSet, Required: true, Desc: "IDs of the vault's services in the bundle."},
		{Name: "ttl_seconds", Kind: kInt, Required: true, Desc: "How long a grant of the bundle lasts."},
		{Name: "version", Kind: kInt, Computed: true, Volatile: true, Desc: "Revision, raised by every change."},
	},
	Create: gop{Method: http.MethodPost, Path: "/access-vaults/{vault_id}/bundles"},
	Read:   gop{Path: "/access-vaults/{vault_id}/bundles"},
	List:   true, ListKey: "items", Paged: true,
	Update:          gop{Method: http.MethodPut, Path: "/access-vaults/{vault_id}/bundles/{id}"},
	ExpectedVersion: "expected_version",
	Delete:          gop{Method: http.MethodDelete, Path: "/access-vaults/{vault_id}/bundles/{id}"},
}

var ingressConnectorSpec = &gspec{
	Name: "ingress_connector",
	Desc: "Publishes apps through an outbound tunnel (for example a Cloudflare Tunnel) running on chosen nodes, " +
		"for hosts that cannot accept inbound connections.",
	Perms: []string{"settings:read", "settings:write"},
	Fields: []gfield{
		{Name: "name", Kind: kString, Required: true, CreateOnly: true, Desc: "Name of the connector."},
		{Name: "provider_type", JSON: "provider", Kind: kString, Required: true, CreateOnly: true, Desc: "Tunnel provider, for example `cloudflare`."},
		{Name: "node_ids", Kind: kIntSet, Required: true, CreateOnly: true, Read: "-", Desc: "IDs of the nodes that run the tunnel."},
		{Name: "host_suffix", Kind: kString, Optional: true, CreateOnly: true, Desc: "Suffix of the hostnames it publishes."},
		{Name: "values", Kind: kJSON, Optional: true, CreateOnly: true, Read: "-", Desc: "Provider settings, for example the account ID."},
		{Name: "secrets", Kind: kStringMap, Optional: true, CreateOnly: true, Sensitive: true, Read: "-", Desc: "Provider credentials."},
		{Name: "auto_publish", Kind: kBool, Optional: true, Desc: "Publish every app's hostnames automatically."},
		{Name: "domain", Kind: kString, Computed: true, Desc: "Domain the connector publishes under."},
		{Name: "status", Kind: kString, Computed: true, Volatile: true, Desc: "Connector status."},
		{Name: "last_error", Kind: kString, Computed: true, Volatile: true, Desc: "Last error."},
	},
	Create: gop{Method: http.MethodPost, Path: "/ingress/connectors"},
	Read:   gop{Path: "/ingress/connectors/{id}"},
	Update: gop{Method: http.MethodPatch, Path: "/ingress/connectors/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/ingress/connectors/{id}"},
}

var lbRouteSpec = &gspec{
	Name:  "load_balancer_route",
	Desc:  "Sends requests for a domain to a fixed host and port, outside any project (a legacy service, another server).",
	Perms: []string{"settings:read", "settings:write"},
	IDKey: "domain",
	Fields: []gfield{
		{Name: "domain", Kind: kString, Required: true, CreateOnly: true, Desc: "Domain the route answers for."},
		{Name: "host", Kind: kString, Required: true, Desc: "Upstream host name or address."},
		{Name: "port", Kind: kInt, Required: true, Desc: "Upstream port."},
		{Name: "route_type", Kind: kString, Optional: true, Desc: "`http` or `tcp`."},
		{Name: "enabled", Kind: kBool, Optional: true, Default: true, Desc: "Serve the route."},
	},
	Create:   gop{Method: http.MethodPost, Path: "/lb/routes"},
	Read:     gop{Path: "/lb/routes/{id}"},
	Update:   gop{Method: http.MethodPut, Path: "/lb/routes/{id}"},
	Delete:   gop{Method: http.MethodDelete, Path: "/lb/routes/{id}"},
	BodyHook: dropOnCreate("enabled"),
	AfterWrite: patchAfterCreateIf("enabled", false, http.MethodPut, "/lb/routes/{id}",
		"host", "port", "route_type"),
}

var networkPolicyRuleSpec = &gspec{
	Name: "network_policy_rule",
	Desc: "Allows or denies traffic between a workload (a project, environment or service) and a peer at the " +
		"network layer of the cluster.",
	Perms: []string{"settings:read", "system:admin"},
	Fields: []gfield{
		{Name: "subject_kind", Kind: kString, Required: true, OneOf: []string{"deployment", "sandbox", "service", "function", "job"},
			Desc: "Kind of workload the rule is about."},
		{Name: "subject_id", Kind: kInt, Required: true, Desc: "ID of that workload."},
		{Name: "direction", Kind: kString, Required: true, OneOf: []string{"ingress", "egress"}, Desc: "Traffic direction."},
		{Name: "action", Kind: kString, Required: true, OneOf: []string{"allow", "deny"}, Desc: "What to do with matching traffic."},
		{Name: "peer_kind", Kind: kString, Required: true, OneOf: []string{"service", "workload", "cidr", "host"}, Desc: "Kind of peer."},
		{Name: "peer_ref", Kind: kString, Required: true, Desc: "The peer: a CIDR, or an ID for other kinds."},
		{Name: "protocol", Kind: kString, Optional: true, Desc: "`tcp`, `udp` or `any`."},
		{Name: "port_start", Kind: kInt, Optional: true, Desc: "First port of the range."},
		{Name: "port_end", Kind: kInt, Optional: true, Desc: "Last port of the range."},
		{Name: "description", Kind: kString, Optional: true, Desc: "Why the rule exists."},
		{Name: "enforceable", Kind: kBool, Computed: true, Volatile: true, Desc: "Whether the nodes can enforce the rule."},
		{Name: "origin", Kind: kString, Computed: true, Desc: "Who created the rule."},
	},
	Create: gop{Method: http.MethodPost, Path: "/network-policy/rules"},
	Read:   gop{Path: "/network-policy/rules/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/network-policy/rules/{id}"},
}

var deliveryProfileSpec = &gspec{
	Name: "delivery_profile",
	Desc: "How traffic reaches apps: `direct` to Temps, or through a CDN (`cloudflare`, `bunny`). Select it for a " +
		"project with `temps_project_delivery_settings`.",
	Perms: []string{"settings:read", "settings:write"},
	Fields: []gfield{
		{Name: "name", Kind: kString, Required: true, Desc: "Name of the profile."},
		{Name: "provider_kind", Kind: kString, Required: true, OneOf: []string{"direct", "cloudflare", "bunny"}, Desc: "Delivery path."},
		{Name: "bunny_api_key", Kind: kString, Optional: true, Sensitive: true, Read: "-", Desc: "Bunny API key (`bunny`)."},
		{Name: "bunny_pull_zone_id", Kind: kInt, Optional: true, Desc: "Bunny pull zone to use (`bunny`)."},
		{Name: "bunny_hostname", Kind: kString, Computed: true, Desc: "Hostname of the Bunny pull zone."},
	},
	Create: gop{Method: http.MethodPost, Path: "/delivery-profiles"},
	Read:   gop{Path: "/delivery-profiles/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/delivery-profiles/{id}"},
}

var projectDeliverySettingsSpec = &gspec{
	Name: "project_delivery_settings",
	Desc: "Which `temps_delivery_profile` a project's traffic uses, with per-environment overrides. Destroying the " +
		"resource returns the project to the instance default.",
	Perms:     []string{"projects:read", "projects:write"},
	Parents:   []string{"project_id"},
	Singleton: true,
	Fields: []gfield{
		{Name: "project_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the project."},
		{Name: "default_profile_id", Kind: kInt, Clearable: true, Desc: "Profile of every environment without an override."},
		{Name: "environment_overrides", Kind: kJSON, Optional: true, Default: "[]",
			Desc: "Per-environment profiles: `jsonencode([{ environment_id = 1, profile_id = 2 }])`."},
	},
	Create:   gop{Method: http.MethodPut, Path: "/projects/{project_id}/delivery-settings"},
	Read:     gop{Path: "/projects/{project_id}/delivery-settings"},
	Update:   gop{Method: http.MethodPut, Path: "/projects/{project_id}/delivery-settings"},
	Delete:   gop{Method: http.MethodPut, Path: "/projects/{project_id}/delivery-settings", Body: map[string]any{"default_profile_id": nil, "environment_overrides": []any{}}},
	BodyHook: sendNulls("default_profile_id"),
}

var clusterNetworkSpec = &gspec{
	Name: "cluster_network",
	Desc: "Address pool and overlay network the nodes of the cluster give workloads. Temps refuses a change while " +
		"`can_change` is false (workloads use the current pool); `change_blocked_by` says why.",
	Perms:     []string{"settings:read", "settings:write"},
	Singleton: true,
	Fields: []gfield{
		{Name: "compute_pool_cidr", Kind: kString, Required: true, Desc: "CIDR workload subnets are taken from."},
		{Name: "subnet_prefix_len", Kind: kInt, Optional: true, Desc: "Prefix length of each node's subnet."},
		{Name: "overlay_network_name", Kind: kString, Optional: true, Desc: "Name of the overlay network."},
		{Name: "can_change", Kind: kBool, Computed: true, Volatile: true, Desc: "Whether the pool can be changed now."},
		{Name: "change_blocked_by", Kind: kStringList, Computed: true, Volatile: true, Desc: "What prevents a change."},
		{Name: "mesh_enabled", Kind: kBool, Computed: true, Volatile: true, Desc: "Whether the WireGuard mesh is on."},
	},
	Create:     gop{Method: http.MethodPut, Path: "/settings/cluster-network", Envelope: "status"},
	Read:       gop{Path: "/settings/cluster-network"},
	Update:     gop{Method: http.MethodPut, Path: "/settings/cluster-network", Envelope: "status"},
	DeleteNote: "Destroying the resource leaves the network as it is; it only stops managing it.",
}

var wireguardMeshSpec = &gspec{
	Name: "wireguard_mesh",
	Desc: "Turns on the encrypted WireGuard mesh between the nodes of the cluster. Temps refuses while " +
		"`can_enable` is false; `enable_blocker` says why. Temps has no endpoint to turn the mesh off, so " +
		"destroying the resource only stops managing it.",
	Perms:     []string{"settings:read", "settings:write"},
	Singleton: true,
	Fields: []gfield{
		{Name: "cidr", Kind: kString, Optional: true, CreateOnly: true, Desc: "Address range of the mesh."},
		{Name: "listen_port", Kind: kInt, Optional: true, CreateOnly: true, Desc: "UDP port WireGuard listens on."},
		{Name: "node_api_port", Kind: kInt, Optional: true, CreateOnly: true, Read: "-", Desc: "Port of the node API over the mesh."},
		{Name: "state", Kind: kString, Computed: true, Volatile: true, Desc: "Mesh state."},
		{Name: "can_enable", Kind: kBool, Computed: true, Volatile: true, Desc: "Whether the mesh can be turned on."},
		{Name: "enable_blocker", Kind: kString, Computed: true, Volatile: true, Desc: "What prevents turning it on."},
	},
	Create: gop{Method: http.MethodPost, Path: "/nodes/wireguard"},
	Read:   gop{Path: "/nodes/wireguard"},
}

var nodeBuildLimitsSpec = &gspec{
	Name: "node_build_limits",
	Desc: "How many builds a node runs at once and how long they may queue and run. Temps has no endpoint to " +
		"read the limits back, so changes made elsewhere are not detected and destroying only stops managing them.",
	Perms:     []string{"settings:write"},
	Parents:   []string{"node_id"},
	Singleton: true,
	Fields: []gfield{
		{Name: "node_id", Kind: kInt, Required: true, Path: true, Desc: "ID of a worker node. The control plane (`0`) " +
			"has no per-node build limits."},
		{Name: "concurrency", Kind: kInt, Required: true, Desc: "Builds running at once."},
		{Name: "queue_wait_seconds", Kind: kInt, Required: true, Desc: "Longest a build waits for a slot."},
		{Name: "build_timeout_seconds", Kind: kInt, Required: true, Desc: "Longest a build may run."},
	},
	Create: gop{Method: http.MethodPut, Path: "/internal/nodes/{node_id}/build-limits"},
	Update: gop{Method: http.MethodPut, Path: "/internal/nodes/{node_id}/build-limits"},
	ReadHook: func(_ context.Context, _ *client.Client, v gvals, _ string) (map[string]any, error) {
		// No read endpoint: keep what was applied.
		return map[string]any{}, nil
	},
}

var nodeResourcesSpec = &gspec{
	Name: "node_resources",
	Desc: "Capacity Temps keeps for itself on a node and whether the scheduler enforces reservations there. " +
		"Destroying the resource leaves the settings as they are.",
	Perms:     []string{"settings:read", "settings:write"},
	Parents:   []string{"node_id"},
	Singleton: true,
	Fields: []gfield{
		{Name: "node_id", Kind: kInt, Required: true, Path: true, ZeroOK: true, Desc: "ID of the node; `0` is the control plane."},
		{Name: "resource_policy", Kind: kString, Optional: true, OneOf: []string{"best_effort", "enforced"},
			Desc: "Whether reservations count against the node's capacity."},
		{Name: "system_reserved_cpu_millis", Kind: kInt, Optional: true, JSON: "system_reserved.cpu_millis",
			Desc: "CPU kept for Temps, in thousandths of a core."},
		{Name: "system_reserved_memory_mb", Kind: kInt, Optional: true, JSON: "system_reserved.memory_mb",
			Desc: "Memory kept for Temps, in MiB."},
	},
	Create: gop{Method: http.MethodPut, Path: "/internal/nodes/{node_id}/resources"},
	Read:   gop{Path: "/internal/nodes/{node_id}/resources"},
	Update: gop{Method: http.MethodPut, Path: "/internal/nodes/{node_id}/resources"},
}

var workloadVolumeSpec = &gspec{
	Name: "workload_volume",
	Desc: "A persistent volume of an environment. Attach it to the environment's containers with " +
		"`temps_workload_volume_attachment`. With `retain_on_delete` the data survives destroying the resource.",
	Perms:   []string{"settings:read", "settings:write"},
	Parents: []string{"environment_id"},
	Fields: []gfield{
		{Name: "environment_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the environment."},
		{Name: "name", Kind: kString, Required: true, Desc: "Name of the volume."},
		{Name: "size_mb", Kind: kInt, Optional: true, Desc: "Size limit in MiB, where the backing supports one."},
		{Name: "retain_on_delete", Kind: kBool, Optional: true, Default: false, Desc: "Keep the data when the volume is deleted."},
		{Name: "volume_id", Kind: kInt, Computed: true, Read: "id", Desc: "Numeric ID of the volume."},
		{Name: "backing", Kind: kString, Computed: true, Desc: "Where the data lives."},
		{Name: "node_id", Kind: kInt, Computed: true, Volatile: true, Desc: "Node the volume is pinned to."},
	},
	Create: gop{Method: http.MethodPost, Path: "/internal/environments/{environment_id}/volumes"},
	Read:   gop{Path: "/internal/environments/{environment_id}/volumes"},
	List:   true, ListKey: "volumes", Paged: true,
	Delete: gop{Method: http.MethodDelete, Path: "/internal/environments/{environment_id}/volumes/{id}"},
}

var workloadVolumeAttachmentSpec = &gspec{
	Name:      "workload_volume_attachment",
	Desc:      "Mounts a `temps_workload_volume` into its environment's containers. Destroying it detaches the volume.",
	Perms:     []string{"settings:read", "settings:write"},
	Parents:   []string{"environment_id", "volume_id"},
	Singleton: true,
	Fields: []gfield{
		{Name: "environment_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the environment."},
		{Name: "volume_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the volume."},
		{Name: "mount_path", Kind: kString, Required: true, Read: "attachment.mount_path", Desc: "Absolute path in the container."},
		{Name: "read_only", Kind: kBool, Optional: true, Default: false, Read: "attachment.read_only", Desc: "Mount it read-only."},
	},
	Create: gop{Method: http.MethodPut, Path: "/internal/environments/{environment_id}/volumes/{volume_id}/attachment"},
	Update: gop{Method: http.MethodPut, Path: "/internal/environments/{environment_id}/volumes/{volume_id}/attachment"},
	Delete: gop{Method: http.MethodDelete, Path: "/internal/environments/{environment_id}/volumes/{volume_id}/attachment"},
	ReadHook: func(ctx context.Context, c *client.Client, v gvals, _ string) (map[string]any, error) {
		return findVolume(ctx, c, v, func(obj map[string]any) bool { return obj["attachment"] == nil })
	},
}

func findVolume(ctx context.Context, c *client.Client, v gvals, gone func(map[string]any) bool) (map[string]any, error) {
	want := scalarString(v["volume_id"])
	for page := 1; page <= 100; page++ {
		raw, err := c.Raw(ctx, http.MethodGet, renderPath("/internal/environments/{environment_id}/volumes", v, ""),
			url.Values{"page": {strconv.Itoa(page)}, "page_size": {"100"}}, nil)
		if client.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		items, _ := asObject(raw)["volumes"].([]any)
		for _, it := range items {
			obj := asObject(it)
			if rawString(obj["id"]) == want {
				if gone(obj) {
					return nil, nil
				}
				return obj, nil
			}
		}
		if len(items) < 100 {
			break
		}
	}
	return nil, nil
}

var sandboxPlacementSpec = &gspec{
	Name:      "sandbox_placement",
	Desc:      "Which nodes may run AI agent sandboxes. Destroying the resource allows every node again.",
	Perms:     []string{"settings:read", "settings:write"},
	Singleton: true,
	Fields: []gfield{
		{Name: "allowed_node_ids", Kind: kIntSet, Required: true, Desc: "IDs of the nodes sandboxes may run on."},
	},
	Create: gop{Method: http.MethodPut, Path: "/v1/sandboxes/placement"},
	Read:   gop{Path: "/v1/sandboxes/placement"},
	Update: gop{Method: http.MethodPut, Path: "/v1/sandboxes/placement"},
	Delete: gop{Method: http.MethodPut, Path: "/v1/sandboxes/placement", Body: map[string]any{"allowed_node_ids": nil}},
}

// dropOnCreate removes keys the create request does not take.
func dropOnCreate(keys ...string) func(map[string]any, gvals, bool) {
	return func(body map[string]any, _ gvals, create bool) {
		if create {
			for _, k := range keys {
				delete(body, k)
			}
		}
	}
}

// patchAfterCreateIf sends one update after create when attribute name was
// configured to value, for a setting the create request cannot carry.
func patchAfterCreateIf(name string, value bool, method, tmpl string, with ...string) func(context.Context, *client.Client, gvals, gvals, string) error {
	return func(ctx context.Context, c *client.Client, plan, prior gvals, id string) error {
		b, ok := plan[name].(types.Bool)
		if prior != nil || !ok || !known(b) || b.ValueBool() != value {
			return nil
		}
		body := map[string]any{name: value}
		for _, k := range with {
			if x := plan[k]; x != nil && known(x) {
				body[k] = scalarJSON(x)
			}
		}
		_, err := c.Raw(ctx, method, renderPath(tmpl, plan, id), nil, body)
		return err
	}
}

func scalarJSON(v attr.Value) any {
	switch x := v.(type) {
	case types.String:
		return x.ValueString()
	case types.Int64:
		return x.ValueInt64()
	case types.Bool:
		return x.ValueBool()
	case types.Float64:
		return x.ValueFloat64()
	}
	return nil
}
