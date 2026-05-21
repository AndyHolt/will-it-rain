locals {
  main_apis = [
    "cloudbilling.googleapis.com",     # read billing account info
    "billingbudgets.googleapis.com",   # budget resource
    "aiplatform.googleapis.com",       # Vertex AI Pipelines, Model Registry
    "run.googleapis.com",              # Cloud Run (backend)
    "cloudfunctions.googleapis.com",   # pipeline trigger function
    "cloudscheduler.googleapis.com",   # weekly pipeline cron
    "artifactregistry.googleapis.com", # container images
    "cloudbuild.googleapis.com",       # image builds (CI/CD)
    "firebase.googleapis.com",         # Firebase project enrolment
    "firebasehosting.googleapis.com",  # frontend hosting
    "monitoring.googleapis.com",       # alert policy + notification channel for pipeline runs
    "pubsub.googleapis.com",           # model-promoted topic → backend refresher
    "eventarc.googleapis.com",         # Pub/Sub → Cloud Function trigger
  ]
}

resource "google_project_service" "main" {
  for_each = toset(local.main_apis)

  service            = each.value
  disable_on_destroy = false
}
