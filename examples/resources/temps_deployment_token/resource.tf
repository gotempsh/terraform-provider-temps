resource "temps_deployment_token" "mailer" {
  project_id  = temps_project.web.id
  name        = "mailer"
  permissions = ["emails:send"]
  expires_at  = "2030-01-01T00:00:00Z"
}

# Hand the token to the app as a secret variable.
resource "temps_environment_variable" "mailer_token" {
  project_id      = temps_project.web.id
  key             = "TEMPS_DEPLOYMENT_TOKEN"
  value           = temps_deployment_token.mailer.token
  environment_ids = [data.temps_environment.production.id]
  is_secret       = true
}
