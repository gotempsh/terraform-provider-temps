# A project deployed from a Dockerfile in a Git repository.
resource "temps_project" "web" {
  name                        = "web"
  source_type                 = "git"
  git_url                     = "https://git.example.com/example/web.git"
  is_public_repo              = true
  main_branch                 = "main"
  preset                      = "dockerfile"
  dockerfile_path             = "Dockerfile"
  exposed_port                = 8080
  automatic_deploy            = true
  enable_preview_environments = false
}
