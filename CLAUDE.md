# CLAUDE.md

`will-it-rain` is a weekly-retrained rainfall classifier for one fixed location
in Scotland. Vertex AI Pipelines for training + Model Registry, Cloud Run +
Firebase Hosting for serving. Which GCP project and region it deploys to is
declared in `config.env` at the repo root — read it there rather than
memorising a value.

uv workspace, single `uv.lock` at root.

## Non-obvious structure

Most of the layout reads fine from `ls`. These parts don't:

- **`will_it_rain_shared/`** is imported by *both* the pipeline (train) and the
  backend (serve). Anything whose drift between the two would break predictions
  belongs in this package — that's the whole reason it exists.
- **`infra/bootstrap/` vs `infra/main/`** — project-level IAM for the terraform
  SA lives in bootstrap, and changes there need a manual
  `terraform -chdir=infra/bootstrap apply` as the human user. See
  [docs/cicd.md](docs/cicd.md) for why the split exists.
- **`model_refresher/`** is a Cloud Function that rolls the backend forward on
  model promotion. Its module docstring explains the design; read that rather
  than inferring from the Terraform.
- **`build/`** holds the compiled pipeline YAML and is gitignored.
- **`config.env`** is the single declared source for `PROJECT_ID`, `REGION` and
  `HOSTING_SITE_ID`. Every consumer reads it natively — the Makefile
  `include`s it, CI loads it via `.github/actions/load-config`, Terraform takes
  it as `TF_VAR_*`, and build/deploy-time Python gets it through
  `will_it_rain_shared/gcp.py`. Nothing generates anything. Add a deploy-target
  value here rather than to a second file.

## Dev workflow

`make help` lists the dev recipes; prefer them over raw `uv run` / `pnpm`
invocations, because they encode the same flags CI uses. `make check` is CI
parity across both stacks and is the thing to run before claiming an edit is
done; `make fix` applies every auto-fixer. Recipes are split `py-*` /
`frontend-*` with aggregators over both.

Recipes that hit GCP are **deliberately absent from `make help`** so they aren't
one tab-complete away, but they exist: `image`, `compile-pipeline`,
`upload-pipeline`, `deploy-pipeline`, `trigger-pipeline-from-local`,
`trigger-pipeline-via-scheduler`, `clean`, `backend-image`, `backend-deploy`,
`backend-go-image`, `frontend-deploy`, `tf-init`, `tf-plan`, `tf-apply`. Read the Makefile for the
current set. `golden-fixtures` is hidden for a different reason — it needs no
GCP, but it retrains a model and rewrites checked-in fixtures.

Run Terraform through `make tf-plan` / `make tf-apply` rather than
`terraform -chdir=…` directly: `project_id`, `region` and `hosting_site_id`
have no defaults, and the recipes are what export them from `config.env`.
`make tf-apply TF_MODULE=infra/bootstrap` for the bootstrap module.

## Gotchas

- **`pydantic-settings` + `ty`**: declare required `BaseSettings` fields as
  `= Field(...)` so the static checker doesn't read `Settings()` as missing
  arguments; the Ellipsis sentinel preserves pydantic's runtime "required"
  behaviour. Worked example in `pipeline/src/pipeline/trigger.py`.
- **Container builds from Apple Silicon**: cross-compile to `linux/amd64`.
  Vertex workers are x86_64 and arm64 images die with "exec format error". The
  `make image` recipe handles this — bypassing it is how you get bitten.
- **Pipeline spec format**: compile to YAML, not JSON. Vertex's parser is less
  reliable on the JSON IR for KFP 2.x. Relatedly, never point the scheduler at
  `templateUri` — it mis-parses KFP 2.x YAML. Keep the inline `pipelineSpec`.
- **Cloud Run doesn't auto-roll on image push.** Revisions pin a digest, so
  every backend code change needs an explicit `make backend-deploy` after the
  image build.
- **Private location config** lives in gitignored `.env`; `.env-example`
  documents the keys (`LATITUDE`, `LONGITUDE`, `COSMOS_UK_SITE_CODE`). Follow
  that layout for new env-driven config. Don't confuse it with `config.env`,
  which is committed: `.env` is for what must stay out of version control,
  `config.env` for what must be *in* it.
- **Serving code must not import `will_it_rain_shared/gcp.py`.** It has no
  defaults, so it raises without `PROJECT_ID` in the environment — which is
  what keeps it build- and deploy-time only. Cloud Run has no reason to set
  `PROJECT_ID`: the backend resolves its project from ADC
  (`google.auth.default()`), which works on Cloud Run, on Vertex and locally.
  Region is the exception — ADC carries no location, so `LOCATION` stays
  injected by `cloud_run.tf`.
- **`frontend/firebase.json` repeats `HOSTING_SITE_ID`** because JSON can't
  interpolate and the Firebase CLI reads the site from that file only (no
  `--site` flag, no env override). Drift is *silent*: with no matching `site`,
  the CLI resolves the project's default site and publishes there instead of
  failing. `make frontend-site-check` guards it, and runs in CI and before
  `frontend-deploy`. The hardcoded `"region"` in the same file is the same
  problem, currently inert.

## Further reading

- [docs/deployment.md](docs/deployment.md) — pipeline trigger, backend and
  frontend deploy procedures
- [docs/cicd.md](docs/cicd.md) — workflows, `deploy.yml` job graph, WIF auth,
  how `config.env` reaches CI, required GitHub secrets.
- [docs/cold-start.md](docs/cold-start.md) — why scale-to-zero latency is the
  common case here, and how to measure it.
- [docs/favicons.md](docs/favicons.md) — which platform reads which icon file,
  and the constraints baked into `scripts/generate-icons.sh`.
