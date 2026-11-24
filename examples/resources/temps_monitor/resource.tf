resource "temps_monitor" "web" {
  project_id             = temps_project.web.id
  environment_id         = data.temps_environment.production.id
  name                   = "web"
  monitor_type           = "health"
  check_path             = "/healthz"
  check_interval_seconds = 60
}
