# Deliberately no defaults for project id and region: these come from config.env
# at the repo root, via TF_VAR_project_id / TF_VAR_region.

variable "project_id" {
  type        = string
  description = "GCP project ID that hosts bootstrap resources. From config.env."
}

variable "region" {
  type        = string
  description = "Default region for regional resources. From config.env."
}

variable "tf_service_account_id" {
  type        = string
  description = "Account ID (the part before @) for the main Terraform service account."
  default     = "terraform"
}

variable "github_repository" {
  type        = string
  description = "GitHub repo in 'owner/name' form allowed to impersonate the Terraform SA via WIF."
  default     = "AndyHolt/will-it-rain"
  validation {
    condition     = can(regex("^[^/]+/[^/]+$", var.github_repository))
    error_message = "github_repository must be in 'owner/name' form."
  }
}

variable "wif_pool_id" {
  type        = string
  description = "ID for the Workload Identity Pool."
  default     = "github-actions"
}

variable "wif_provider_id" {
  type        = string
  description = "ID for the OIDC provider inside the pool."
  default     = "github"
}
