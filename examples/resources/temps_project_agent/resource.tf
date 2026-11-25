resource "temps_project_agent" "triage" {
  project_id = temps_project.web.id
  slug       = "triage"
  name       = "Error triage"
  prompt     = "Investigate new errors and open a pull request with a fix."
  enabled    = false
}
