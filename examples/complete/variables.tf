# SPDX-FileCopyrightText: 2024-2026 Temps Contributors
# SPDX-License-Identifier: MIT OR Apache-2.0

variable "name_prefix" {
  description = "Prefix for every object this example creates."
  type        = string
  default     = "example"
}

variable "backup_endpoint" {
  description = "S3-compatible endpoint backups are written to, for example https://s3.example.com."
  type        = string
}

variable "backup_bucket" {
  description = "Existing bucket for backups."
  type        = string
  default     = "temps-backups"
}

variable "backup_access_key_id" {
  type      = string
  sensitive = true
}

variable "backup_secret_key" {
  type      = string
  sensitive = true
}

variable "api_token" {
  description = "Example secret injected into the web project."
  type        = string
  sensitive   = true
  default     = "change-me"
}

variable "deletion_protection" {
  description = "Protect the database from terraform destroy. Set to false to tear the example down."
  type        = bool
  default     = true
}
