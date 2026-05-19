# Repo-wide recipes: dev tooling (lint/format/typecheck/test) + pipeline
# build/compile/upload/trigger.
#
# Run from the repo root. Pipeline-side targets also assume:
#   - gcloud is authenticated and `gcloud auth configure-docker
#     europe-west2-docker.pkg.dev` has been run once.
#   - Docker (with buildx) is running locally.
#   - uv has the workspace synced.

PROJECT_ID            := will-it-rain-496215
REGION                := europe-west2
ARTEFACTS_BUCKET      := $(PROJECT_ID)-model-artefacts
IMAGE_REPO            := $(REGION)-docker.pkg.dev/$(PROJECT_ID)/will-it-rain-images
IMAGE_NAME            := pipeline
IMAGE_TAG             ?= latest
BACKEND_IMAGE_NAME    := backend
BACKEND_IMAGE_TAG     ?= latest
BACKEND_SERVICE       := backend
PIPELINE_SPEC         := build/pipeline.yaml
IMAGE_SENTINEL        := build/.image-pushed
BACKEND_IMAGE_SENTINEL := build/.backend-image-pushed
BACKEND_DEV_PORT       ?= 8080

# Files baked into the pipeline image. If any of these change, the image
# must be rebuilt before the next Vertex run, otherwise `:latest` will
# serve stale code. The YAML spec depends on a narrower set (see below)
# because @dsl.component captures only the wrapper function bodies.
IMAGE_SOURCES    := pipeline/Dockerfile \
		    pyproject.toml uv.lock \
		    pipeline/pyproject.toml \
		    will_it_rain_shared/pyproject.toml \
		    $(shell find pipeline/src -name '*.py') \
		    $(shell find will_it_rain_shared/src -name '*.py')

BACKEND_IMAGE_SOURCES := backend/Dockerfile \
			 pyproject.toml uv.lock \
			 backend/pyproject.toml \
			 will_it_rain_shared/pyproject.toml \
			 $(shell find backend/src -name '*.py') \
			 $(shell find will_it_rain_shared/src -name '*.py')

# Files baked into the model-refresher zip. Same staged-artifact pattern as
# the pipeline YAML: CI zips + uploads to a fixed GCS path,
# infra/main/model_refresh.tf reads the object's generation to trigger a
# redeploy on change.
MODEL_REFRESHER_SOURCE_DIR := model_refresher
MODEL_REFRESHER_ZIP        := build/model_refresher.zip
MODEL_REFRESHER_SOURCES    := $(shell find $(MODEL_REFRESHER_SOURCE_DIR) -type f)

.DEFAULT_GOAL := help

.PHONY: help \
	py-lint py-format-check py-typecheck py-test py-check \
	py-lint-fix py-format py-fix \
	frontend-lint frontend-format-check frontend-typecheck frontend-check \
	frontend-lint-fix frontend-format frontend-fix \
	check fix prek \
	backend-dev frontend-dev dev \
	image compile-pipeline upload-pipeline deploy-pipeline trigger-run clean \
	backend-image backend-deploy \
	model-refresher-source upload-model-refresher-source \
	frontend-build frontend-deploy

# ---------------------------------------------------------------------------
# Dev tooling
# ---------------------------------------------------------------------------
#
# Python tooling (ruff, ty, pytest) covers the uv workspace: pipeline, backend,
# shared library. Frontend tooling (biome, tsc) covers the TypeScript workspace
# under frontend/. `check` and `fix` aggregate both.

# Print available targets (anything whose recipe line carries a `## doc`).
help:
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# Python — read-only checks (no mutations) — what CI runs.
py-lint: ## ruff lint, no fixes
	uv run ruff check

py-format-check: ## ruff format --check, no writes
	uv run ruff format --check

py-typecheck: ## ty static type checks
	uv run ty check

py-test: ## pytest across the workspace
	uv run pytest

py-check: py-lint py-format-check py-typecheck py-test ## all Python read-only checks

# Python — auto-fixers (mutate the working tree).
py-lint-fix: ## ruff lint with --fix
	uv run ruff check --fix

py-format: ## ruff format (writes)
	uv run ruff format

py-fix: py-lint-fix py-format ## all Python auto-fixers

# Frontend — read-only checks. `biome check` covers both lint and format-check
# in one pass; the split frontend-lint / frontend-format-check recipes are
# for targeted use.
frontend-lint: ## biome lint, no fixes
	pnpm -C frontend lint

frontend-format-check: ## biome format --check, no writes
	pnpm -C frontend format

frontend-typecheck: ## tsc -b (project references)
	pnpm -C frontend typecheck

frontend-check: ## biome check (lint + format) + tsc
	pnpm -C frontend check
	pnpm -C frontend typecheck

# Frontend — auto-fixers (mutate the working tree).
frontend-lint-fix: ## biome lint --write
	pnpm -C frontend lint:fix

frontend-format: ## biome format --write
	pnpm -C frontend format:fix

frontend-fix: ## biome check --write (lint + format)
	pnpm -C frontend check:fix

# Aggregators across both toolchains. Default for "did I break anything?".
check: py-check frontend-check ## all read-only checks (CI parity, both stacks)

fix: py-fix frontend-fix ## all auto-fixers (both stacks)

# Run the full prek hook set against every file. Matches what pre-commit
# enforces locally and what the prek hook job runs in CI.
prek: ## prek run --all-files
	prek run --all-files

# ---------------------------------------------------------------------------
# Local dev servers
# ---------------------------------------------------------------------------
#
# Each restart re-downloads the @production .joblib from GCS. Requires gcloud
# ADC (`gcloud auth application-default login`) for Vertex + GCS access.
backend-dev: ## reloading FastAPI server on $(BACKEND_DEV_PORT)
	uv run --package backend uvicorn backend.main:app \
	    --reload \
	    --reload-dir backend/src \
	    --reload-dir will_it_rain_shared/src \
	    --host 127.0.0.1 \
	    --port $(BACKEND_DEV_PORT)

# Vite dev server with HMR. The vite config proxies /api/* to the backend at
# 127.0.0.1:8080, so the frontend hits same-origin URLs and no CORS config is
# needed on the backend.
frontend-dev: ## Vite dev server (proxies /api -> backend-dev)
	pnpm -C frontend dev

# Convenience: run both dev servers in parallel. `make -j2` lets Ctrl-C take
# down both children together.
dev: ## run backend-dev and frontend-dev together (make -j2)
	$(MAKE) -j2 backend-dev frontend-dev

# ---------------------------------------------------------------------------
# Pipeline build / compile / deploy
# ---------------------------------------------------------------------------

## (targets below are documented inline; pipeline ops aren't shown in `make help`)

# Build and push the pipeline component image. Vertex AI workers are x86_64;
# building on Apple Silicon without --platform produces an arm64 image that
# fails on the Vertex side with "exec format error". buildx + --platform
# cross-compiles cleanly, and --push uploads directly (no local load step,
# which doesn't work for cross-platform builds anyway).
#
# The sentinel file records the last successful push. Downstream targets
# (trigger-run, deploy-pipeline) depend on it so they rebuild + push when
# any IMAGE_SOURCES file is newer than the sentinel.
image: $(IMAGE_SENTINEL)

$(IMAGE_SENTINEL): $(IMAGE_SOURCES)
	docker buildx build \
	    --platform linux/amd64 \
	    --push \
	    --tag $(IMAGE_REPO)/$(IMAGE_NAME):$(IMAGE_TAG) \
	    --file pipeline/Dockerfile \
	    .
	@mkdir -p $(dir $@) && touch $@

# Compile the KFP pipeline definition to YAML (the canonical KFP 2.x IR format;
# Vertex AI's parser handles JSON output less reliably than YAML).
compile-pipeline: $(PIPELINE_SPEC)

$(PIPELINE_SPEC): pipeline/src/pipeline/pipeline.py pipeline/src/pipeline/kfp_components.py
	@mkdir -p $(dir $@)
	uv run --package pipeline python -c "from kfp import compiler; from pipeline.pipeline import will_it_rain_pipeline; compiler.Compiler().compile(will_it_rain_pipeline, '$@')"

# Upload the compiled pipeline to the GCS path the schedule resource points at.
upload-pipeline: $(PIPELINE_SPEC)
	gcloud storage cp $(PIPELINE_SPEC) gs://$(ARTEFACTS_BUCKET)/pipelines/will-it-rain.yaml

# End-to-end: rebuild image, recompile pipeline, upload.
deploy-pipeline: image upload-pipeline

# Submit a one-off pipeline run via the aiplatform SDK. Depends on both
# the compiled spec and a current image — without the latter, Vertex would
# pull a stale :latest and changes to component bodies (e.g. train.py)
# would silently fail to take effect.
trigger-run: $(PIPELINE_SPEC) $(IMAGE_SENTINEL)
	uv run --package pipeline python -m pipeline.trigger

# ---------------------------------------------------------------------------
# Backend build / deploy
# ---------------------------------------------------------------------------

# Build and push the backend image. Same cross-compile reasoning as `image`:
# Cloud Run runs x86_64.
backend-image: $(BACKEND_IMAGE_SENTINEL)

$(BACKEND_IMAGE_SENTINEL): $(BACKEND_IMAGE_SOURCES)
	docker buildx build \
	    --platform linux/amd64 \
	    --push \
	    --tag $(IMAGE_REPO)/$(BACKEND_IMAGE_NAME):$(BACKEND_IMAGE_TAG) \
	    --file backend/Dockerfile \
	    .
	@mkdir -p $(dir $@) && touch $@

# Roll out a new Cloud Run revision pointing at the freshly-pushed image.
# Use this after `backend-image` to pick up code or promoted-model changes.
backend-deploy: $(BACKEND_IMAGE_SENTINEL)
	gcloud run services update $(BACKEND_SERVICE) \
	    --region=$(REGION) \
	    --image=$(IMAGE_REPO)/$(BACKEND_IMAGE_NAME):$(BACKEND_IMAGE_TAG)

# ---------------------------------------------------------------------------
# Model-refresher build / upload
# ---------------------------------------------------------------------------

# Zip the model_refresher source. `cd` into the source dir so the archive
# contains main.py / requirements.txt at the top level — Cloud Functions
# expects them there, not nested under model_refresher/.
model-refresher-source: $(MODEL_REFRESHER_ZIP)

$(MODEL_REFRESHER_ZIP): $(MODEL_REFRESHER_SOURCES)
	@mkdir -p $(dir $@)
	cd $(MODEL_REFRESHER_SOURCE_DIR) && zip -r ../$(MODEL_REFRESHER_ZIP) . -x '__pycache__/*' '*.pyc'

# Upload the zip to the GCS path the model_refresh.tf data source reads.
# Same staging pattern as `upload-pipeline`.
upload-model-refresher-source: $(MODEL_REFRESHER_ZIP)
	gcloud storage cp $(MODEL_REFRESHER_ZIP) gs://$(ARTEFACTS_BUCKET)/functions/model_refresher.zip

clean:
	rm -rf build/

# ---------------------------------------------------------------------------
# Frontend build / deploy
# ---------------------------------------------------------------------------

# Build the SPA into frontend/dist. Firebase Hosting deploy publishes from
# that directory (configured in frontend/firebase.json).
frontend-build:
	pnpm -C frontend install --frozen-lockfile
	pnpm -C frontend build

# Publish the built SPA to Firebase Hosting. TF owns the site shape (project
# enrolment, site, /api/** rewrite to Cloud Run); this just ships content,
# mirroring the backend-deploy pattern. Requires `firebase login` once.
frontend-deploy: frontend-build
	cd frontend && firebase deploy --only hosting
