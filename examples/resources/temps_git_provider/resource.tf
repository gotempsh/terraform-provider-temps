# A self-hosted Git server reached with an access token.
resource "temps_git_provider" "internal" {
  kind      = "generic"
  name      = "Internal Git"
  clone_url = "https://git.example.com"
  token     = var.git_token
}
