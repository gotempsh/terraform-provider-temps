resource "temps_skill" "release_notes" {
  slug    = "release-notes"
  name    = "Release notes"
  content = file("${path.module}/skills/release-notes.md")
}
