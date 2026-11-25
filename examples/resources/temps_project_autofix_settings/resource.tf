resource "temps_project_autofix_settings" "web" {
  project_id      = temps_project.web.id
  min_confidence  = 4
  protected_paths = ["migrations/**"]
  test_command    = "make test"
}
