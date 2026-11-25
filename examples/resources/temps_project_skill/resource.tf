resource "temps_project_skill" "release_notes" {
  project_id = temps_project.web.id
  slug       = "release-notes"
  name       = "Release notes"
  content    = file("${path.module}/skills/release-notes.md")
}
