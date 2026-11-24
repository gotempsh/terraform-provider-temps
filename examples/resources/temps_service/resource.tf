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

  timeouts {
    create = "20m"
  }
}
