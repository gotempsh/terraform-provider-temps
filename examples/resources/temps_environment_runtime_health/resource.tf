resource "temps_environment_runtime_health" "staging" {
  project_id     = temps_project.web.id
  environment_id = temps_environment.staging.id
  enabled        = true
}
