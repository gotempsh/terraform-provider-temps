resource "temps_environment_domain" "staging" {
  project_id     = temps_project.web.id
  environment_id = temps_environment.staging.id
  domain         = "staging.example.com"
}
