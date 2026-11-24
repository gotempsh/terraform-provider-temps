# Connection variables a linked project receives (for example POSTGRES_URL).
data "temps_service_connection" "db" {
  service_id = temps_service.db.id
  project_id = temps_project.web.id
}
