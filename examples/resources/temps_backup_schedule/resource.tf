resource "temps_backup_schedule" "nightly" {
  name                = "nightly"
  description         = "Nightly database backups"
  schedule_expression = "0 0 3 * * *" # seconds-first cron: daily at 03:00
  retention_period    = 7
  s3_source_id        = temps_backup_destination.s3.id
  service_ids         = [temps_service.db.id]
  tags                = ["nightly"]
}
