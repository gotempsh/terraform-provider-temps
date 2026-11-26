# A project belongs to at most one group: creating this fails if the project
# is already in another group.
resource "temps_project_group_membership" "web" {
  group_id   = temps_project_group.web.id
  project_id = temps_project.web.id

  # Build from the group's repository and branch, in the project's own
  # directory.
  inherit_repository = true
}
