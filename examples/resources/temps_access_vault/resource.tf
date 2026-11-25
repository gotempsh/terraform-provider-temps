resource "temps_access_vault" "finance" {
  name        = "Finance"
  service_ids = [temps_access_service.billing_api.id]
}
