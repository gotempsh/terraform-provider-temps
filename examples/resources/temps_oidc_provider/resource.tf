resource "temps_oidc_provider" "sso" {
  name          = "Company SSO"
  issuer_url    = "https://idp.example.com"
  client_id     = "temps"
  client_secret = var.oidc_client_secret
  scopes        = "openid email profile"
  enabled       = true
}
