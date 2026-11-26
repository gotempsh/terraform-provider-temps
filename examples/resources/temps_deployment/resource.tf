resource "temps_project" "web" {
  name        = "web"
  source_type = "docker_image"
  preset      = "dockerfile"
}

data "temps_environment" "production" {
  project_id = temps_project.web.id
  name       = "production"
}

# Changing image (or triggers) deploys again; the new deployment replaces
# the running one once it is healthy.
resource "temps_deployment" "web" {
  project_id        = temps_project.web.id
  environment_id    = data.temps_environment.production.id
  image             = "ghcr.io/example-org/web:1.4.2"
  health_check_path = "/healthz"

  timeouts {
    create = "20m"
  }
}

output "web_url" {
  value = temps_deployment.web.url
}
