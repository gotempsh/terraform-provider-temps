data "temps_environment" "production" {
  project_id = temps_project.web.id
  name       = "production"
}

# Alert when production sees more than 100 errors of one issue in an hour.
resource "temps_error_alert_rule" "burst" {
  project_id       = temps_project.web.id
  name             = "error burst"
  trigger_type     = "frequency"
  trigger_config   = jsonencode({ count = 100, window_minutes = 60 })
  environment_id   = data.temps_environment.production.id
  priority         = "Critical"
  cooldown_minutes = 15
}
