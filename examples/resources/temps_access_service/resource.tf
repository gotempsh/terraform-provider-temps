resource "temps_access_service" "billing_api" {
  name            = "Billing API"
  max_ttl_seconds = 1800
  credential      = var.billing_api_token
  policy = jsonencode({
    origin        = "https://billing.example.com"
    methods       = ["GET"]
    path_prefixes = ["/v1/invoices"]
  })
}
