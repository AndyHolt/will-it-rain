# CI/CD

Workflows under `.github/workflows/`:

| Workflow             | Triggers                    | What it does |
|----------------------|-----------------------------|--------------|
| `prek.yml`           | PR + push to main           | prek hook set (ruff, ty, biome, gitleaks, …) |
| `pytest.yml`         | PR + push to main           | `uv run pytest` across the workspace |
| `go.yml`             | PR + push touching `backend-go/**` or `Makefile` | `make go-test` |
| `terraform-plan.yml` | PR touching `infra/main/**` or `config.env` | `terraform plan`, no apply |
| `deploy.yml`         | push to main                | path-filtered build + apply + deploy |

## `deploy.yml`

Each job is gated on the path filter for its component:

1. `changes` — dorny/paths-filter → `pipeline` / `backend_go` / `frontend` /
   `infra` / `model_refresher` booleans.
2. `build-pipeline-image`, `build-backend-go-image` —
   `docker buildx … --push :latest`.
3. `compile-and-upload-pipeline` — `make compile-pipeline` + `make upload-pipeline`.
4. `terraform-apply` — runs when pipeline, infra **or** model_refresher changed.
   Waits on the upload because `scheduler.tf` reads the GCS YAML at plan time.
   Backend and frontend code changes don't alter TF state, so they don't
   trigger apply.
5. `backend-deploy` — `gcloud run services update` against the
   `backend-go:latest` tag `build-backend-go-image` just pushed. It gates on
   `backend_go`, and is named for the service it updates rather than the image
   it deploys.
6. `frontend-deploy` — `make frontend-site-check` + `make frontend-build` +
   `npx firebase-tools deploy --only hosting --project …`.

The deploy jobs call `gcloud` directly rather than `make backend-deploy`: that
recipe depends on the image sentinel under `build/`, which doesn't exist on a
fresh runner, so make would rebuild the image the build job just pushed.

`backend_go`'s filter is narrower than `pipeline`'s — no
`will_it_rain_shared/**`, no `pyproject.toml`, no `uv.lock`. `backend-go/` is a
self-contained Go module and none of those are inputs to its image. Tests for
it run in `go.yml`, not here; `deploy.yml` only builds and rolls it.

`config.env` is in **every** path filter. It names the project everything
deploys to, so a change to it invalidates every artefact — images would land in
a different registry, buckets and the Cloud Run services move. Without it, a
change of project would merge as a no-op.

## Auth

WIF impersonating the `terraform@…` SA. That SA's project roles live in
`infra/bootstrap/main.tf`, not `infra/main/`, to avoid the chicken-and-egg of
granting them through the config they're needed to apply.

Bootstrap also grants the SA `roles/billing.viewer` on the project's billing
account — `google_billing_budget` needs it to refresh, and it's a
billing-account-level permission rather than a project one. So whoever applies
bootstrap needs billing-account admin rights. Bootstrap is applied manually, as
the human user, infrequently.

## Config

Which project, region and Hosting site CI targets comes from `config.env` at
the repo root, not from workflow `env:` blocks. Jobs that need it run
`./.github/actions/load-config` after checkout; that composite action appends
the assignments to `$GITHUB_ENV` and additionally emits `TF_VAR_project_id`,
`TF_VAR_region` and `TF_VAR_hosting_site_id`, because Terraform spells those
names differently. The Makefile does the same mapping for local runs.

Two details worth knowing before editing that action:

- It filters `config.env` to assignment lines rather than `cat`-ing it.
  `$GITHUB_ENV` rejects any line without an `=`, so the comments would break
  the whole file.
- Jobs that only shell out to `make` don't need the action — `make` `include`s
  `config.env` itself. Only jobs using `${{ env.PROJECT_ID }}`-style
  expressions, or running `terraform`, load it.

Neither module defaults `project_id` or `region`, so a job that forgets the
action fails with "No value for required variable" rather than applying
somewhere unintended.

`WIF_PROVIDER` and `WIF_SERVICE_ACCOUNT` stay literal in `deploy.yml`: the
provider path embeds the project *number*, which `config.env` doesn't carry.

## Required GitHub secrets

Set on the repo: `WEATHER_LATITUDE`, `WEATHER_LONGITUDE`, `COSMOS_UK_SITE_CODE`,
`NOTIFICATION_EMAIL`. Passed to TF as `TF_VAR_*` env vars; mirrors the gitignored
`infra/main/terraform.tfvars` used locally.

The project and region are deliberately *not* secrets — they aren't sensitive,
and they need to be reviewable in a diff. They live in `config.env`.
