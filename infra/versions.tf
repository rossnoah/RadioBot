terraform {
  required_version = ">= 1.10"

  # Remote state in S3 so the deployment survives loss of the local machine.
  # Bucket name/region/key live in backend.hcl, which is NOT committed (the
  # bucket name embeds the AWS account ID and this repo is public). Run
  # ./bootstrap-state.sh to (re)generate it from your AWS credentials — it
  # also creates the bucket if needed and migrates any local state. From a
  # fresh clone that one script is all you need to reconnect.
  backend "s3" {}

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    archive = {
      source  = "hashicorp/archive"
      version = "~> 2.4"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

provider "aws" {
  region = var.aws_region
}
