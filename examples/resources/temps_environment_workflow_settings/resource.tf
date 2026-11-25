resource "temps_environment_workflow_settings" "production" {
  project_id          = temps_project.web.id
  environment_id      = data.temps_environment.production.id
  max_concurrent_runs = 8
  retention_days      = 14
}
