resource "temps_access_service" "billing_api" {
  name            = "Billing API"
  enabled         = true
  max_ttl_seconds = 1800
  credential      = var.billing_api_token
  policy = jsonencode({
    origin        = "https://billing.example.com"
    methods       = ["GET"]
    path_prefixes = ["/v1/invoices"]
  })
}

# A service that belongs to one project.
resource "temps_access_service" "web_search" {
  project_id      = temps_project.web.id
  name            = "Search API"
  enabled         = true
  max_ttl_seconds = 900
  credential      = var.search_api_token
  policy = jsonencode({
    origin  = "https://search.example.com"
    methods = ["GET", "POST"]
  })
}
