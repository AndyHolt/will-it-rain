data "google_project" "this" {
  project_id = var.project_id
  depends_on = [google_project_service.main]
}

resource "google_billing_budget" "monthly" {
  billing_account = data.google_project.this.billing_account
  display_name    = "will-it-rain monthly"

  depends_on = [google_project_service.main]

  budget_filter {
    projects = ["projects/${data.google_project.this.number}"]
  }

  amount {
    specified_amount {
      currency_code = "GBP"
      units         = "10"
    }
  }

  threshold_rules {
    threshold_percent = 0.5
  }
  threshold_rules {
    threshold_percent = 0.9
  }
  threshold_rules {
    threshold_percent = 1.0
  }
}
