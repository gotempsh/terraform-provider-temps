resource "temps_settings" "this" {
  values = jsonencode({
    preview_domain  = "preview.example.com"
    image_retention = { enabled = true, default_hours = 336 }
  })
}
