resource "temps_kv_settings" "this" {
  container_name = "temps-kv-main"
}

resource "temps_kv" "this" {
  max_memory = "256mb"
  depends_on = [temps_kv_settings.this]
}
