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

variable "pipeline_template_uri" {
  type        = string
  description = "GCS URI of the compiled pipeline JSON. Override in CI to pin per-commit."
  default     = "gs://will-it-rain-496215-model-artefacts/pipelines/will-it-rain.yaml"
}

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
  description = "Container image (incl. tag) for the Cloud Run backend. Override in CI to pin per-commit."
  default     = "europe-west2-docker.pkg.dev/will-it-rain-496215/will-it-rain-images/backend:latest"
}
