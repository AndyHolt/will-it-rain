# ---------------------------------------------------------------------------
# No defaults for project id, region and hosting site id: these come from
# config.env at the repo root, enforced as a single source of truth
# ---------------------------------------------------------------------------

variable "project_id" {
  type        = string
  description = "GCP project ID. From config.env."
}

variable "region" {
  type        = string
  description = "Default region for regional resources. From config.env."
}

# Not derived from project_id: the site id is the public hostname
# (https://<id>.web.app), so tying it to the project would move the URL on
# every project migration. frontend/firebase.json repeats the value — JSON
# can't interpolate — and the two must agree.
variable "hosting_site_id" {
  type        = string
  description = "Firebase Hosting site ID; becomes <id>.web.app. From config.env."
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

# Defaults to the :latest image in this project's Artifact Registry repo. That
# can't be written as `default = "${var.region}-…"` — a variable default must be
# a constant expression — so it's null here and derived in the local below.
#
# Named for the image, which is `backend-go` after the source directory that
# builds it, not for the service it runs on, which is `backend`. The Python
# `backend` image and the var that pointed at it are gone.
variable "backend_go_image" {
  type        = string
  description = "Container image (incl. tag) for the backend Cloud Run service. Override in CI to pin per-commit; null derives it from project_id and region."
  default     = null
}

locals {
  # coalesce skips null, so an explicit var.backend_go_image (CI pinning a
  # per-commit tag) still wins over the derived default.
  backend_go_image = coalesce(
    var.backend_go_image,
    "${var.region}-docker.pkg.dev/${var.project_id}/will-it-rain-images/backend-go:latest",
  )
}
