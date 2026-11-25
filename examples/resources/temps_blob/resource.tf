resource "temps_blob_settings" "this" {
  container_name = "temps-blob-main"
}

resource "temps_blob" "this" {
  depends_on = [temps_blob_settings.this]
}
