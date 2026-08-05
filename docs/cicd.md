# CI/CD

Workflows under `.github/workflows/`:

| Workflow             | Triggers                    | What it does |
|----------------------|-----------------------------|--------------|
| `prek.yml`           | PR + push to main           | prek hook set (ruff, ty, biome, gitleaks, …) |
| `pytest.yml`         | PR + push to main           | `uv run pytest` across the workspace |
| `terraform-plan.yml` | PR touching `infra/main/**` | `terraform plan`, no apply |
| `deploy.yml`         | push to main                | path-filtered build + apply + deploy |

## `deploy.yml`

Each job is gated on the path filter for its component:

1. `changes` — dorny/paths-filter → `pipeline` / `backend` / `frontend` / `infra` booleans.
2. `build-pipeline-image`, `build-backend-image` — `docker buildx … --push :latest`.
3. `compile-and-upload-pipeline` — `make compile-pipeline` + `make upload-pipeline`.
4. `terraform-apply` — runs when pipeline **or** infra changed. Waits on the
   upload because `scheduler.tf` reads the GCS YAML at plan time. Backend and
   frontend code changes don't alter TF state, so they don't trigger apply.
5. `backend-deploy` — `make backend-deploy` (`gcloud run services update`).
6. `frontend-deploy` — `make frontend-build` + `npx firebase-tools deploy --only hosting`.

## Auth

WIF impersonating the `terraform@…` SA. That SA's project roles live in
`infra/bootstrap/main.tf`, not `infra/main/`, to avoid the chicken-and-egg of
granting them through the config they're needed to apply.

Bootstrap also grants the SA `roles/billing.viewer` on the project's billing
account — `google_billing_budget` needs it to refresh, and it's a
billing-account-level permission rather than a project one. So whoever applies
bootstrap needs billing-account admin rights. Bootstrap is applied manually, as
the human user, infrequently.

## Required GitHub secrets

Set on the repo: `WEATHER_LATITUDE`, `WEATHER_LONGITUDE`, `COSMOS_UK_SITE_CODE`,
`NOTIFICATION_EMAIL`. Passed to TF as `TF_VAR_*` env vars; mirrors the gitignored
`infra/main/terraform.tfvars` used locally.
