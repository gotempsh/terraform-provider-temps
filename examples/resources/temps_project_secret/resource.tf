variable "stripe_key" {
  type      = string
  sensitive = true
}

data "temps_environment" "production" {
  project_id = temps_project.web.id
  name       = "production"
}

# Only production deployments receive the key.
resource "temps_project_secret" "stripe" {
  project_id      = temps_project.web.id
  key             = "STRIPE_SECRET_KEY"
  value           = var.stripe_key
  environment_ids = [data.temps_environment.production.id]
}
