terraform {
  required_version = ">= 1.6.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
  }

  # Remote state in the bucket created by infra/bootstrap.
  # Backend blocks cannot reference variables, so the bucket name is hardcoded.
  backend "gcs" {
    bucket = "will-it-rain-496308-tfstate"
    prefix = "main"
  }
}
