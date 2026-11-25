resource "temps_project_dsn" "frontend" {
  project_id     = temps_project.web.id
  name           = "frontend"
  environment_id = data.temps_environment.production.id
}

output "frontend_dsn" {
  value     = temps_project_dsn.frontend.dsn
  sensitive = true
}
