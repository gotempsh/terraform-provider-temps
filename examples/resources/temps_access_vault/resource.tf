resource "temps_access_vault" "finance" {
  name        = "Finance"
  service_ids = [temps_access_service.billing_api.id]
}

# A vault that belongs to one project; Temps links it to the project's sandboxes.
resource "temps_access_vault" "web" {
  project_id  = temps_project.web.id
  name        = "Web"
  service_ids = [temps_access_service.web_search.id]
}
