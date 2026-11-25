resource "temps_environment_ingress" "staging" {
  project_id     = temps_project.web.id
  environment_id = temps_environment.staging.id
  mode           = "enforce"
  require_login  = true
}
