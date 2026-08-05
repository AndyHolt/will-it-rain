# CLAUDE.md

`will-it-rain` is a weekly-retrained rainfall classifier for one fixed location
in Scotland. Vertex AI Pipelines for training + Model Registry, Cloud Run +
Firebase Hosting for serving. GCP project `will-it-rain-496215`, region
`europe-west2`.

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

## Dev workflow

`make help` lists the dev recipes; prefer them over raw `uv run` / `pnpm`
invocations, because they encode the same flags CI uses. `make check` is CI
parity across both stacks and is the thing to run before claiming an edit is
done; `make fix` applies every auto-fixer. Recipes are split `py-*` /
`frontend-*` with aggregators over both.

Recipes that hit GCP are **deliberately absent from `make help`** so they aren't
one tab-complete away, but they exist: `image`, `compile-pipeline`,
`upload-pipeline`, `deploy-pipeline`, `trigger-pipeline-from-local`,
`trigger-pipeline-via-scheduler`, `clean`, `backend-image`, `backend-deploy`.
Read the Makefile for the current set.

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
  that layout for new env-driven config.

## Further reading

- [docs/deployment.md](docs/deployment.md) — pipeline trigger, backend and
  frontend deploy procedures
- [docs/cicd.md](docs/cicd.md) — workflows, `deploy.yml` job graph, WIF auth,
  required GitHub secrets.
