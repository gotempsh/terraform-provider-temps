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

variable "domain" {
  description = "Custom domain for production; point its DNS at the Temps server."
  type        = string
  default     = "app.example.com"
}

variable "webhook_url" {
  description = "Endpoint that receives deployment events."
  type        = string
  default     = "https://hooks.example.com/temps"
}

variable "office_cidrs" {
  description = "Networks allowed to reach staging, in canonical CIDR form."
  type        = list(string)
  default     = ["198.51.100.0/24"]
}

variable "terraform_runs_outside_office" {
  description = "Set to true when Terraform runs from an address outside office_cidrs (for example CI), so Temps accepts a policy that excludes it."
  type        = bool
  default     = true
}
