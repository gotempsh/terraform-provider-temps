resource "temps_ai_project_limits" "web" {
  project_id              = temps_project.web.id
  max_requests_per_minute = 60
}
