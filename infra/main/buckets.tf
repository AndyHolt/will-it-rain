resource "google_storage_bucket" "model_artefacts" {
  name     = "${var.project_id}-model-artefacts"
  location = var.region

  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = false

  versioning {
    enabled = true
  }
}
