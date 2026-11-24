# Every project has a production environment.
data "temps_environment" "production" {
  project_id = data.temps_project.web.id
  name       = "production"
}
