resource "temps_project_group" "web" {
  name        = "Web apps"
  slug        = "web-apps"
  description = "Front ends that share settings"

  # The monorepo the group's projects can follow (see inherit_repository on
  # temps_project_group_membership).
  repository = {
    repo_owner                 = "example-org"
    repo_name                  = "web"
    main_branch                = "main"
    git_provider_connection_id = 1
  }
}
