resource "temps_backup_destination" "s3" {
  name             = "backups"
  bucket_name      = "temps-backups"
  bucket_path      = "production"
  endpoint         = "https://s3.example.com"
  region           = "us-east-1"
  force_path_style = true
  access_key_id    = var.backup_access_key_id
  secret_key       = var.backup_secret_key
  is_default       = true

  # Check the credentials and bucket on every apply.
  verify_on_apply = true
}
