resource "temps_access_bundle" "read" {
  vault_id    = temps_access_vault.finance.id
  name        = "Read invoices"
  service_ids = [temps_access_service.billing_api.id]
  ttl_seconds = 900
}
