resource "temps_error_saved_search" "timeouts" {
  project_id = temps_project.web.id
  name       = "Unresolved timeouts"
  shared     = true
  filters    = jsonencode({ status = "unresolved", search = "timeout" })
}
