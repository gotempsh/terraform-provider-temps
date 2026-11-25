resource "temps_project_identical_builds" "web" {
  project_id = temps_project.web.id
  mode       = "share"
}
