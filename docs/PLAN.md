# Terraform provider for Temps

## Goal
Operators describe a Temps installation as code: projects with environments and variables, managed databases linked to projects, backup destinations and schedules, uptime monitors with public status pages. `plan` shows changes, `apply` makes them, `import` adopts existing objects, and a second `plan` after apply reports no changes. Out of scope for v1: deployments, analytics, error tracking, nodes, users, teams.

## Shape
- Repo `gotempsh/terraform-provider-temps` (private until first release); registry address `registry.terraform.io/gotempsh/temps` (OpenTofu too).
- Go + terraform-plugin-framework (not SDKv2), scaffolding-framework layout.
- Client in `internal/client`, typed, covering only used endpoints, written against `apps/temps-cli/openapi.json`.
- Provider config: `endpoint` (env TEMPS_ENDPOINT; `/api` appended) and `api_key` (sensitive; env TEMPS_API_KEY). Never log the key; request timeout; retry only connection errors, 429, 502–504 with bounded backoff; map RFC 7807 problem+json errors to diagnostics keeping title, detail and resource ID.

## Resources (v1) — IDs are server integer IDs as strings; all importable (projects also by slug)
- `temps_project`: POST /projects, GET/PUT/DELETE /projects/{id}, POST /projects/{id}/settings, PATCH /projects/{id}/source. Git source, preset, build/install commands, port, auto-deploy. Force replacement only where the API cannot update in place.
- `temps_environment`: POST/GET /projects/{pid}/environments, GET/DELETE …/{env_id}, PUT …/{env_id}/settings. The default production environment is exposed via the `temps_environment` data source, not managed twice.
- `temps_environment_variable`: /projects/{pid}/env-vars, PUT/DELETE …/{var_id}. key, value (sensitive), environment_ids, is_secret, include_in_preview. Secret values are write-only server-side: keep configured value in state, never report drift on it; on TF ≥ 1.11 also offer `value_wo` + `value_wo_version`. is_secret false→true updates, true→false forces replacement. Read by list+filter (no single GET).
- `temps_service`: POST /external-services, GET/PUT/DELETE /external-services/{id}, GET /external-services/types/{type}/parameters. Postgres, Redis, MongoDB, MariaDB, S3/RustFS. service_type, name, topology, node_id force replacement; version/docker_image upgrade in place; parameters is a map with sensitive values. Creation is async: poll GET (and /health) until running with a `timeouts {}` block (default 15 min). `deletion_protection` defaults to true.
- `temps_service_link`: POST /external-services/{id}/projects, DELETE /external-services/{id}/projects/{project_id}. database_provisioning_mode, custom_database_name. Any change replaces.
- `temps_backup_destination`: /backups/s3-sources CRUD, POST …/{id}/set-default, POST …/test. Bucket, path, region, endpoint, path-style, TLS, is_default. access_key_id/secret_key sensitive and masked on read (no drift). Optional `verify_on_apply`.
- `temps_backup_schedule`: /backups/schedules CRUD, PATCH …/{id}/enable|disable, /backups/schedules/{id}/services. schedule_expression, retention_period, backup_type, s3_source_id (replaces), service_ids or target_all_services, include_control_plane, tags, enabled.
- `temps_monitor`: POST/GET /projects/{pid}/monitors, GET/PATCH/DELETE /monitors/{id}. monitor_type, check_path, check_interval_seconds (server clamps to ≥30; validate client-side instead).
- `temps_status_page`: /status-pages CRUD, POST …/publish|unpublish, PUT …/password, PUT …/subscriptions. title, description, ordered monitors (monitor_id, display_name, group_name), appearance, published, optional password (sensitive, write-only), subscription settings.
- `temps_status_page_domain`: /status-pages/{id}/domains, …/verify. Exposes DNS records to create and verification state.

## Data sources
temps_project (id or slug), temps_environment (project + name), temps_service (id or name), temps_service_types, temps_service_connection (variables a linked service injects, sensitive).

## Behaviour rules
- No perpetual diffs: server-defaulted attrs Computed + UseStateForUnknown; normalise/validate server-normalised values; every resource's acceptance test asserts an empty plan after apply.
- Secrets: all credential attributes Sensitive; masked responses (`***`, has_secret) never overwrite state; write-only attributes where supported.
- Deletes: destroying temps_service with deletion_protection=true fails with a diagnostic explaining how to allow it. 404 on read removes from state; 404 on delete is success.
- Async waits bounded by `timeouts`, typed timeout diagnostics naming the resource and last status, respect context cancellation.
- Docs list the API-key permissions each resource needs; a 403 diagnostic names the missing permission.
- Generic names only in examples/fixtures.

## Milestones
1. Scaffold, client, provider config, temps_project, temps_environment, temps_environment_variable (+ unit and acceptance tests).
2. temps_service, temps_service_link, service data sources.
3. temps_backup_destination, temps_backup_schedule.
4. temps_monitor, temps_status_page, temps_status_page_domain.
5. Docs, examples (incl. examples/complete), CI, GoReleaser config (no signing/publishing).
