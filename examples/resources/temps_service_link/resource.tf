# Deployments of the project receive the connection variables of the service.
resource "temps_service_link" "web_db" {
  service_id = temps_service.db.id
  project_id = temps_project.web.id
}
