# Blocks a network for one project.
resource "temps_shield_entry" "scanner" {
  project_id = temps_project.web.id
  network    = "198.51.100.0/24"
  verdict    = "block"
  note       = "Credential stuffing"
  expires_at = "2030-01-01T00:00:00Z"
}

# Logs a TLS fingerprint on every project.
resource "temps_shield_entry" "bot" {
  ja4     = "t13d1516h2_8daaf6152771_02713d6af862"
  verdict = "log"
}
