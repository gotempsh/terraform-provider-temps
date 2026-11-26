<!--
SPDX-FileCopyrightText: 2024-2026 Temps Contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Terraform provider for Temps

Describe a [Temps](https://github.com/gotempsh/temps) installation as code:
projects with environments and variables, managed databases and backups,
monitors, status pages and alerts, domains, DNS and email, traffic and security
rules, users, single sign-on and Git connections, the AI gateway, and instance and
cluster settings.
`plan` shows changes, `apply` makes them, `import` adopts existing objects, and a
second `plan` after `apply` reports no changes.

The design and its scope are in [`docs/PLAN.md`](docs/PLAN.md). Generated
reference documentation for every resource and data source is in [`docs/`](docs).

| Area | Resources |
|---|---|
| Projects | `temps_project`, `temps_environment`, `temps_environment_variable`, `temps_project_secret`, `temps_project_group`, `temps_project_group_membership`, `temps_project_group_variable`, `temps_project_access`, `temps_project_internal_access`, `temps_team`, `temps_team_member`, `temps_project_dsn`, `temps_project_identical_builds`, `temps_project_container_security`, `temps_project_compose_security`, `temps_project_group_route` |
| Environments | `temps_environment_domain`, `temps_environment_ingress`, `temps_environment_runtime_health`, `temps_workload_volume`, `temps_workload_volume_attachment`, `temps_environment_workflow_settings` |
| Databases and backups | `temps_service`, `temps_service_link`, `temps_backup_destination`, `temps_backup_schedule`, `temps_kv`, `temps_kv_settings`, `temps_blob`, `temps_blob_settings` |
| Monitoring and alerts | `temps_monitor`, `temps_cron_monitor`, `temps_http_check`, `temps_status_page`, `temps_status_page_domain`, `temps_error_alert_rule`, `temps_error_inbound_filter`, `temps_notification_provider`, `temps_notification_provider_slack`, `temps_notification_provider_email`, `temps_notification_provider_telegram`, `temps_notification_provider_cloudflare`, `temps_notification_route`, `temps_notification_preferences`, `temps_webhook`, `temps_status_page_maintenance`, `temps_status_page_webhook`, `temps_error_ownership_rules`, `temps_error_saved_search`, `temps_error_attachment_quota` |
| Domains, DNS and email | `temps_custom_domain`, `temps_domain`, `temps_dns_provider`, `temps_dns_provider_domain`, `temps_email_provider`, `temps_email_domain`, `temps_email_domain_project` |
| Traffic and security | `temps_ip_access_policy`, `temps_global_ip_access_rule`, `temps_edge_rule`, `temps_shield_entry`, `temps_shield_token`, `temps_shield_settings`, `temps_load_balancer_route`, `temps_ingress_connector`, `temps_network_policy_rule`, `temps_delivery_profile`, `temps_project_delivery_settings`, `temps_ingress_hostname`, `temps_node_public_ingress`, `temps_cloudflare_access_provider`, `temps_admin_gate_settings` |
| Identity and access | `temps_user`, `temps_user_role`, `temps_oidc_provider`, `temps_oidc_role_mapping`, `temps_api_key`, `temps_deployment_token`, `temps_git_provider`, `temps_global_secret`, `temps_access_service`, `temps_access_vault`, `temps_access_bundle`, `temps_access_vault_link` |
| AI | `temps_ai_provider`, `temps_ai_provider_model`, `temps_ai_instance_limits`, `temps_ai_project_limits`, `temps_project_agent`, `temps_mcp_server`, `temps_skill`, `temps_project_mcp_server`, `temps_project_skill`, `temps_project_autofix_settings`, `temps_ai_model_catalog_settings`, `temps_ai_coding_agent_settings` |
| Instance and cluster | `temps_settings`, `temps_feature_flag`, `temps_feature_flag_environment`, `temps_cluster_network`, `temps_wireguard_mesh`, `temps_node_build_limits`, `temps_node_resources`, `temps_sandbox_placement`, `temps_preview_gateway_settings`, `temps_telemetry_settings` |
| Product analytics | `temps_funnel`, `temps_survey` |

Data sources: `temps_environment`, `temps_project`, `temps_project_group`, `temps_service`, `temps_service_connection`, `temps_service_types`, `temps_webhook_event_types`.

## Requirements

- Terraform 1.11 or newer (write-only attributes such as `value_wo`), or OpenTofu.
- A Temps server, v0.1.0-beta.62 or newer. Older servers have no status pages.
  Status page passwords and subscription settings need a server that provides
  `PUT /status-pages/{id}/password` and `/subscriptions`; on older servers the
  provider reports that the endpoint is missing.
- An API key (`tk_...`). Each resource's documentation lists the permissions it
  needs; a key with the `admin` role has all of them. A 403 names the missing one.
- Go 1.27 to build from source.

## Usage

```terraform
terraform {
  required_providers {
    temps = {
      source = "gotempsh/temps"
    }
  }
}

# Or set TEMPS_ENDPOINT and TEMPS_API_KEY.
provider "temps" {
  endpoint = "https://temps.example.com"
  api_key  = var.temps_api_key
}
```

[`examples/complete`](examples/complete) configures a project with a staging
environment and variables, a PostgreSQL database linked to it, nightly backups to
an S3-compatible bucket, and a health monitor on a published status page.

Things worth knowing:

- Backup schedules use the server's **seconds-first** cron: `0 0 3 * * *` is
  daily at 03:00. A five-field expression is rejected at plan time with the
  six-field equivalent.
- `temps_service` has `deletion_protection = true` by default; set it to `false`
  and apply before destroying, because destroying a service deletes its data.
- A backup destination that still holds backups can only be destroyed with
  `force_destroy = true`, which deletes those backups first.
- Every project comes with a `production` environment: read it with the
  `temps_environment` data source rather than managing it.
- Secrets never cause drift: masked values returned by the server do not
  overwrite the configured ones. Prefer `value_wo` / `password_wo` to keep
  secrets out of the state.

## Developing

```sh
go build -o bin/terraform-provider-temps .
go test ./...            # unit tests against an in-memory Temps fake
golangci-lint run ./...
go generate ./...        # regenerates docs/ from the schema and examples/
```

Unit tests drive the real Terraform CLI (it must be on `PATH`) against an
in-memory fake of the Temps API; they need no server.

### Using a local build

Point Terraform at the binary with a development override in your CLI
configuration (`~/.terraformrc`, or a file named by `TF_CLI_CONFIG_FILE`):

```hcl
provider_installation {
  dev_overrides {
    "gotempsh/temps" = "/path/to/terraform-provider-temps/bin"
  }
  direct {}
}
```

Skip `terraform init` while the override is active; `plan` and `apply` use the
local binary directly.

### Running against a local Temps

1. Start PostgreSQL with TimescaleDB, for example in Docker, bound to loopback:

   ```sh
   docker run -d --name temps-dev-db -p 127.0.0.1:15432:5432 \
     -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=temps \
     timescale/timescaledb-ha:pg18
   ```

2. Download a Temps release binary, verify its checksum, and start it:

   ```sh
   printf '%s' 'a-long-admin-password' > admin-password
   TEMPS_ADMIN_EMAIL=admin@example.com TEMPS_ADMIN_PASSWORD_FILE=./admin-password \
   temps serve \
     --database-url postgres://postgres:postgres@127.0.0.1:15432/temps \
     --data-dir ./temps-data --disable-https-redirect \
     --address 127.0.0.1:8081 --tls-address 127.0.0.1:8443 \
     --console-address 127.0.0.1:8080
   ```

   The two `TEMPS_ADMIN_*` variables create the first administrator on first
   boot. Run `temps serve --help` for the flags of your version.

3. Sign in to the console at `http://127.0.0.1:8080`, create an API key with the
   `admin` role, and export it:

   ```sh
   export TEMPS_ENDPOINT=http://127.0.0.1:8080
   export TEMPS_API_KEY=tk_...
   ```

4. Apply the complete example (set `deletion_protection = false` so it can be
   destroyed again):

   ```sh
   cd examples/complete
   terraform apply -var deletion_protection=false \
     -var backup_endpoint=http://127.0.0.1:9000 -var backup_bucket=temps-backups \
     -var backup_access_key_id=... -var backup_secret_key=...
   terraform plan -detailed-exitcode   # exit code 0: no changes
   terraform destroy -var deletion_protection=false ...
   ```

   The backup variables point at any S3-compatible bucket the Temps server can
   reach; a local RustFS or MinIO container works.

### Acceptance tests

Acceptance tests (`TestAcc*`) create, import, update, replace and destroy real
objects on the server named by `TEMPS_ENDPOINT` / `TEMPS_API_KEY`, and assert an
empty plan after every apply. Use a disposable server.

```sh
export TF_ACC=1
export TEMPS_ACC_S3_ENDPOINT=http://127.0.0.1:9000   # bucket for backup tests,
export TEMPS_ACC_S3_BUCKET=temps-backups               # reachable from the server
export TEMPS_ACC_S3_ACCESS_KEY_ID=...
export TEMPS_ACC_S3_SECRET_KEY=...
go test ./internal/provider -run TestAcc -count=1 -timeout 60m -v
```

They create managed PostgreSQL services, so the server needs Docker.

## Releasing

[`.goreleaser.yml`](.goreleaser.yml) builds archives in the layout the Terraform
registry expects (`goreleaser release --snapshot --clean`). It does not sign or
publish anything.

## License

Licensed under either of [Apache License, Version 2.0](LICENSE-APACHE) or
[MIT license](LICENSE-MIT) at your option, like Temps itself.
