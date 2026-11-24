# SPDX-FileCopyrightText: 2024-2026 Temps Contributors
# SPDX-License-Identifier: MIT OR Apache-2.0

# A web project built from a Dockerfile, with a staging environment and variables.
resource "temps_project" "web" {
  name            = "${var.name_prefix}-web"
  source_type     = "docker_image"
  preset          = "dockerfile"
  dockerfile_path = "Dockerfile"
  exposed_port    = 8080
}

# Temps creates a production environment with every project.
data "temps_environment" "production" {
  project_id = temps_project.web.id
  name       = "production"
}

resource "temps_environment" "staging" {
  project_id = temps_project.web.id
  name       = "staging"
  branch     = "develop"
}

resource "temps_environment_variable" "log_level" {
  project_id      = temps_project.web.id
  key             = "LOG_LEVEL"
  value           = "info"
  environment_ids = [data.temps_environment.production.id, temps_environment.staging.id]
}

resource "temps_environment_variable" "api_token" {
  project_id      = temps_project.web.id
  key             = "API_TOKEN"
  value           = var.api_token
  is_secret       = true
  environment_ids = [data.temps_environment.production.id]
}

# A managed PostgreSQL database, linked to the project so deployments get POSTGRES_URL & co.
resource "temps_service" "db" {
  name         = "${var.name_prefix}-db"
  service_type = "postgres"
  parameters = {
    database        = "app"
    username        = "app"
    max_connections = "50"
  }
  deletion_protection = var.deletion_protection
}

resource "temps_service_link" "web_db" {
  service_id = temps_service.db.id
  project_id = temps_project.web.id
}

# Nightly backups of the database to an S3-compatible bucket, kept for a week.
resource "temps_backup_destination" "s3" {
  name             = "${var.name_prefix}-backups"
  bucket_name      = var.backup_bucket
  bucket_path      = var.name_prefix
  endpoint         = var.backup_endpoint
  force_path_style = true
  access_key_id    = var.backup_access_key_id
  secret_key       = var.backup_secret_key
  verify_on_apply  = true
  force_destroy    = !var.deletion_protection
}

resource "temps_backup_schedule" "nightly" {
  name                = "${var.name_prefix}-nightly"
  schedule_expression = "0 0 3 * * *" # seconds-first cron: daily at 03:00
  retention_period    = 7
  s3_source_id        = temps_backup_destination.s3.id
  service_ids         = [temps_service.db.id]
  tags                = ["terraform"]
}

# An uptime monitor on production, shown on a published status page.
resource "temps_monitor" "web" {
  project_id     = temps_project.web.id
  environment_id = data.temps_environment.production.id
  name           = "web"
  monitor_type   = "health"
  check_path     = "/healthz"
}

resource "temps_status_page" "public" {
  title       = "Example status"
  description = "Current status of the example services."
  published   = true

  monitors = [
    {
      monitor_id   = temps_monitor.web.id
      display_name = "Web"
      group_name   = "Core"
    },
  ]

  appearance = {
    theme = "auto"
  }
}

# The domain, the deploy notifications and the scheduled job the app runs.
resource "temps_custom_domain" "web" {
  project_id     = temps_project.web.id
  environment_id = data.temps_environment.production.id
  domain         = var.domain
}

resource "temps_webhook" "deploys" {
  project_id = temps_project.web.id
  url        = var.webhook_url
  events     = ["deployment.succeeded", "deployment.failed"]
}

resource "temps_cron_monitor" "nightly_report" {
  project_id = temps_project.web.id
  slug       = "nightly-report"
  schedule   = { crontab = "0 3 * * *" }
}

# A feature flag that is on in staging only.
resource "temps_feature_flag" "new_checkout" {
  project_id    = temps_project.web.id
  key           = "new-checkout"
  value_type    = "bool"
  default_value = jsonencode(false)
}

resource "temps_feature_flag_environment" "new_checkout_staging" {
  project_id     = temps_project.web.id
  key            = temps_feature_flag.new_checkout.key
  environment_id = temps_environment.staging.id
  value          = jsonencode(true)
}

# Staging is reachable from the office network only.
resource "temps_ip_access_policy" "staging" {
  project_id          = temps_project.web.id
  environment_id      = temps_environment.staging.id
  mode                = "allow"
  cidrs               = [for c in var.office_cidrs : { cidr = c, description = "Office" }]
  acknowledge_lockout = var.terraform_runs_outside_office
}

# The platform team deploys the project.
resource "temps_team" "platform" {
  name = "Platform"
  slug = "platform"
}

resource "temps_project_access" "platform_web" {
  project_id = temps_project.web.id
  team_id    = temps_team.platform.id
  role       = "deployer"
}
