resource "google_cloud_run_v2_service" "backend" {
  name                = "backend"
  location            = var.region
  deletion_protection = false

  template {
    service_account                  = google_service_account.backend.email
    max_instance_request_concurrency = 80

    scaling {
      # min=1 keeps one warm instance to avoid the ~20s cold-start path
      # (container boot + lightgbm/aiplatform imports + Vertex Model.list +
      # GCS .joblib download). Idle CPU is throttled outside requests, so
      # ongoing cost is mostly the pinned 1Gi of memory. Drop back to 0
      # once the model is baked into the image and cold start is fast.
      min_instance_count = 1
      max_instance_count = 3
    }

    containers {
      image = var.backend_image

      ports {
        container_port = 8080
      }

      resources {
        limits = {
          cpu    = "1"
          memory = "1Gi"
        }
        # Free during the startup window; doubles available CPU while
        # imports + model load run.
        startup_cpu_boost = true
      }

      env {
        name  = "PROJECT"
        value = var.project_id
      }
      env {
        name  = "LOCATION"
        value = var.region
      }
      env {
        name  = "MODEL_DISPLAY_NAME"
        value = var.model_display_name
      }
      env {
        name  = "LATITUDE"
        value = var.weather_latitude
      }
      env {
        name  = "LONGITUDE"
        value = var.weather_longitude
      }
    }
  }

  depends_on = [google_project_service.main]
}

# Plan calls for fully-public access (no auth). The frontend will hit this
# via Firebase Hosting rewrites, but the URL itself is reachable directly.
# TODO once frontend deployed, lock down backend so only reachable via frontend
resource "google_cloud_run_v2_service_iam_member" "backend_public" {
  name     = google_cloud_run_v2_service.backend.name
  location = google_cloud_run_v2_service.backend.location
  role     = "roles/run.invoker"
  member   = "allUsers"
}

output "backend_url" {
  value       = google_cloud_run_v2_service.backend.uri
  description = "Public URL of the backend Cloud Run service."
}
