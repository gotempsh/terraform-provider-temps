# A project belongs to at most one group: adding it here moves it out of any
# other group.
resource "temps_project_group_membership" "web" {
  group_id   = temps_project_group.web.id
  project_id = temps_project.web.id
}
