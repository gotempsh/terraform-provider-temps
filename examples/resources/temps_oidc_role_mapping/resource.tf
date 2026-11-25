resource "temps_oidc_role_mapping" "admins" {
  provider_id = temps_oidc_provider.sso.id
  idp_group   = "platform-admins"
  role        = "admin"
  priority    = 10
}
