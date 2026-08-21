output "backup_endpoint_url" {
  description = "Lambda Function URL — set as backup.endpoint_url in config.yaml"
  value       = aws_lambda_function_url.backup.function_url
}

output "device_secret" {
  description = "Shared secret — set as backup.secret in config.yaml (view with: tofu output -raw device_secret)"
  value       = random_password.device_secret.result
  sensitive   = true
}

output "bucket_name" {
  description = "S3 bucket holding the backups"
  value       = aws_s3_bucket.backup.bucket
}
