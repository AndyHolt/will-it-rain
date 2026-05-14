resource "google_artifact_registry_repository" "images" {
  repository_id = "will-it-rain-images"
  location      = var.region
  format        = "DOCKER"
  description   = "Container images for will-it-rain pipeline components and backend."

  depends_on = [google_project_service.main]
}
