resource "temps_access_vault_link" "web" {
  vault_id   = temps_access_vault.finance.id
  project_id = temps_project.web.id
}
