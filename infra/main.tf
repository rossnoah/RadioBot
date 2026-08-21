data "aws_caller_identity" "current" {}

locals {
  bucket_name = "${var.name_prefix}-${data.aws_caller_identity.current.account_id}"
  key_prefix  = "radiobot/"
}

# ============================================================================
# S3 bucket — versioned, private, lifecycle-managed
# ============================================================================

resource "aws_s3_bucket" "backup" {
  bucket = local.bucket_name
}

resource "aws_s3_bucket_public_access_block" "backup" {
  bucket = aws_s3_bucket.backup.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Versioning means an overwrite (e.g. the daily DB snapshot, or an attacker
# re-PUTting an existing key) never destroys the previous copy.
resource "aws_s3_bucket_versioning" "backup" {
  bucket = aws_s3_bucket.backup.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "backup" {
  bucket = aws_s3_bucket.backup.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "backup" {
  bucket = aws_s3_bucket.backup.id

  rule {
    id     = "abort-incomplete-multipart"
    status = "Enabled"

    filter {}

    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }

  rule {
    id     = "expire-noncurrent-versions"
    status = "Enabled"

    filter {}

    noncurrent_version_expiration {
      noncurrent_days = var.noncurrent_version_expiration_days
    }
  }
}

# ============================================================================
# DynamoDB — daily upload quota counters
# ============================================================================

resource "aws_dynamodb_table" "quota" {
  name         = "${var.name_prefix}-quota"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"

  attribute {
    name = "pk"
    type = "S"
  }

  ttl {
    attribute_name = "expires_at"
    enabled        = true
  }
}

# ============================================================================
# Device secret — the only credential that lives on the SDR box
# ============================================================================

resource "random_password" "device_secret" {
  length  = 48
  special = false
}

# ============================================================================
# Lambda — authenticates the device, enforces quota, issues presigned POSTs
# ============================================================================

resource "aws_iam_role" "lambda" {
  name = "${var.name_prefix}-lambda"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "lambda" {
  name = "${var.name_prefix}-lambda"
  role = aws_iam_role.lambda.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "PutBackupObjects"
        Effect   = "Allow"
        Action   = "s3:PutObject"
        Resource = "${aws_s3_bucket.backup.arn}/${local.key_prefix}*"
      },
      {
        Sid      = "QuotaCounter"
        Effect   = "Allow"
        Action   = "dynamodb:UpdateItem"
        Resource = aws_dynamodb_table.quota.arn
      },
      {
        Sid    = "Logs"
        Effect = "Allow"
        Action = [
          "logs:CreateLogGroup",
          "logs:CreateLogStream",
          "logs:PutLogEvents",
        ]
        Resource = "arn:aws:logs:${var.aws_region}:${data.aws_caller_identity.current.account_id}:*"
      },
    ]
  })
}

data "archive_file" "lambda_zip" {
  type        = "zip"
  source_file = "${path.module}/lambda/handler.py"
  output_path = "${path.module}/build/handler.zip"
}

resource "aws_lambda_function" "backup_auth" {
  function_name = "${var.name_prefix}-auth"
  role          = aws_iam_role.lambda.arn
  runtime       = "python3.13"
  handler       = "handler.handler"
  timeout       = 10
  memory_size   = 128

  filename         = data.archive_file.lambda_zip.output_path
  source_code_hash = data.archive_file.lambda_zip.output_base64sha256

  environment {
    variables = {
      BUCKET_NAME         = aws_s3_bucket.backup.bucket
      QUOTA_TABLE         = aws_dynamodb_table.quota.name
      DEVICE_SECRET       = random_password.device_secret.result
      KEY_PREFIX          = local.key_prefix
      MAX_UPLOADS_PER_DAY = tostring(var.max_uploads_per_day)
      MAX_BYTES_PER_DAY   = tostring(var.max_bytes_per_day)
      MAX_OBJECT_BYTES    = tostring(var.max_object_bytes)
    }
  }
}

resource "aws_lambda_function_url" "backup" {
  function_name      = aws_lambda_function.backup_auth.function_name
  authorization_type = "NONE" # auth is the x-backup-secret header, checked in the handler
}

resource "aws_lambda_permission" "public_url" {
  statement_id           = "AllowPublicFunctionUrl"
  action                 = "lambda:InvokeFunctionUrl"
  function_name          = aws_lambda_function.backup_auth.function_name
  principal              = "*"
  function_url_auth_type = "NONE"
}

# ============================================================================
# Optional cost budget alert (created only if budget_alert_email is set)
# ============================================================================

resource "aws_budgets_budget" "backup" {
  count = var.budget_alert_email != "" ? 1 : 0

  name         = "${var.name_prefix}-monthly"
  budget_type  = "COST"
  limit_amount = tostring(var.budget_limit_usd)
  limit_unit   = "USD"
  time_unit    = "MONTHLY"

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.budget_alert_email]
  }
}
