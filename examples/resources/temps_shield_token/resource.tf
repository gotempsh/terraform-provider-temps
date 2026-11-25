resource "temps_shield_token" "ci" {
  project_id     = temps_project.web.id
  environment_id = temps_environment.staging.id
  name           = "ci"
}

output "shield_token" {
  value     = temps_shield_token.ci.secret
  sensitive = true
}
