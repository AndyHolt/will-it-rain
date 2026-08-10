data "google_project" "this" {
  project_id = var.project_id
}

locals {
  # Alert whenever spend crosses any of these fractions of the budget, on
  # either basis — the rules below are the full cross product.
  budget_alert_fractions   = [0.1, 0.5, 0.9, 1.0]
  budget_alert_spend_bases = ["CURRENT_SPEND", "FORECASTED_SPEND"]
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

  # `threshold_rules` is a list to Terraform, not a set, so this ordering is
  # part of the diff: changing the order of either local above rewrites the
  # budget for no behavioural gain.
  dynamic "threshold_rules" {
    for_each = [
      for pair in setproduct(local.budget_alert_spend_bases, local.budget_alert_fractions) :
      { spend_basis = pair[0], threshold_percent = pair[1] }
    ]
    iterator = rule

    content {
      spend_basis       = rule.value.spend_basis
      threshold_percent = rule.value.threshold_percent
    }
  }
}
