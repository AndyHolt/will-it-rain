variable "project_id" {
  type        = string
  description = "GCP project ID."
  default     = "will-it-rain-496215"
}

variable "region" {
  type        = string
  description = "Default region for regional resources."
  default     = "europe-west2"
}

# ---------------------------------------------------------------------------
# Private location/site config — supplied via gitignored terraform.tfvars
# locally, or via TF_VAR_<name> env vars in CI.
# ---------------------------------------------------------------------------

variable "weather_latitude" {
  type        = number
  description = "Latitude of the forecast / station location (decimal degrees)."
}

variable "weather_longitude" {
  type        = number
  description = "Longitude of the forecast / station location (decimal degrees)."
}

variable "cosmos_uk_site_code" {
  type        = string
  description = "COSMOS UK weather station site code (used for observations fetch)."
}

variable "notification_email" {
  type        = string
  description = "Recipient of pipeline-run alert emails (success/failure/cancelled)."
}

# ---------------------------------------------------------------------------
# Non-secret config with sensible defaults.
# Override via TF_VAR_<name> in CI to pin per-commit artefacts, etc.
# ---------------------------------------------------------------------------

variable "training_window_start_date" {
  type        = string
  description = "Earliest date to fetch forecast/observations data from (ISO date)."
  default     = "2023-05-12"
}

variable "model_display_name" {
  type        = string
  description = "Display name for the model in the Vertex AI Model Registry."
  default     = "will-it-rain"
}

variable "backend_image" {
  type        = string
  description = "Container image (incl. tag) for the Cloud Run backend. Defaults to :latest in this project's Artifact Registry repo (see local.backend_image); override in CI to pin per-commit."
  default     = null
}

variable "hosting_site_id" {
  type        = string
  description = "Firebase Hosting site ID, which fixes the public URL at https://<site_id>.web.app. Deliberately independent of project_id so the URL survives a project migration."
  default     = "will-it-rain-496215"
}
