# Enrol the GCP project with Firebase and provision a Hosting site.
#
# Deploy split mirrors the backend: TF owns the shape (project enrolment,
# site existence, /api/** rewrite to Cloud Run); `firebase deploy --only
# hosting` (wrapped by `make frontend-deploy`) ships the built `dist/` as
# new versions/releases.

resource "google_firebase_project" "this" {
  provider = google-beta

  depends_on = [google_project_service.main]
}

# One site is enough for v1; if we ever need preview channels with custom
# hostnames we'd add more sites here. The site ID is its own variable rather
# than var.project_id: it's the public hostname, so it shouldn't move every
# time the project does.
resource "google_firebase_hosting_site" "default" {
  provider = google-beta

  site_id = var.hosting_site_id

  depends_on = [google_firebase_project.this]
}

output "hosting_default_url" {
  value       = "https://${google_firebase_hosting_site.default.site_id}.web.app"
  description = "Default Firebase Hosting URL for the SPA."
}
