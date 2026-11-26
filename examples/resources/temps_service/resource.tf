resource "temps_service" "db" {
  name         = "db"
  service_type = "postgres"
  parameters = {
    database        = "app"
    username        = "app"
    max_connections = "100"
  }

  # Refuse terraform destroy until this is set to false.
  deletion_protection = true

  # Healthcheck overrides; values left out inherit the instance default.
  # They apply when the container is next recreated, or now (restarting the
  # service briefly) with apply_immediately.
  health_check = {
    interval_secs = 30
    timeout_secs  = 5
    retries       = 5
  }

  # Offer TLS while still accepting plaintext. Changing it restarts the database.
  tls_mode = "available"

  # Cap container log ingestion; 0 lifts a limit, left out uses the platform budget.
  log_ingestion = {
    lines_per_sec = 2000
  }

  timeouts {
    create = "20m"
  }
}
