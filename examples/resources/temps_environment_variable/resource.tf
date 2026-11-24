resource "temps_environment_variable" "log_level" {
  project_id      = temps_project.web.id
  key             = "LOG_LEVEL"
  value           = "info"
  environment_ids = [data.temps_environment.production.id]
}

# A secret kept out of the Terraform state (Terraform 1.11+): bump
# value_wo_version to push a new value.
resource "temps_environment_variable" "api_token" {
  project_id       = temps_project.web.id
  key              = "API_TOKEN"
  value_wo         = var.api_token
  value_wo_version = 1
  is_secret        = true
  environment_ids  = [data.temps_environment.production.id]
}
