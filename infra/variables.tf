variable "aws_region" {
  description = "AWS region for all resources"
  type        = string
  default     = "us-east-1"
}

variable "name_prefix" {
  description = "Prefix for resource names"
  type        = string
  default     = "radiobot-backup"
}

variable "max_uploads_per_day" {
  description = "Maximum number of presigned uploads the device may request per UTC day"
  type        = number
  default     = 50000
}

variable "max_bytes_per_day" {
  description = "Maximum total bytes the device may upload per UTC day (default 2 GiB)"
  type        = number
  default     = 2147483648
}

variable "max_object_bytes" {
  description = "Maximum size of a single uploaded object (default 512 MiB, sized for DB snapshots)"
  type        = number
  default     = 536870912
}

variable "noncurrent_version_expiration_days" {
  description = "Days to keep old (overwritten) object versions before deleting them"
  type        = number
  default     = 90
}

variable "budget_alert_email" {
  description = "Optional email for a monthly AWS cost budget alert. Leave empty to skip creating the budget."
  type        = string
  default     = ""
}

variable "budget_limit_usd" {
  description = "Monthly cost budget in USD for the alert (only used if budget_alert_email is set)"
  type        = number
  default     = 5
}
