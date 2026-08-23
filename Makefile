# Repo-wide recipes: dev tooling (lint/format/typecheck/test) + pipeline
# build/compile/upload/trigger.
#
# Run from the repo root. Pipeline-side targets also assume:
#   - gcloud is authenticated and `gcloud auth configure-docker
#     $(REGION)-docker.pkg.dev` has been run once.
#   - Docker (with buildx) is running locally.
#   - uv has the workspace synced.

# Project and region are declared once, in config.env at the repo root, and
# shared with Terraform, CI and the deploy-time Python entry points. Exported
# so recipes that shell out to Python (which reads them via
# will_it_rain_shared.gcp) see the same values make does.
include config.env
export PROJECT_ID REGION HOSTING_SITE_ID

# Terraform reads its input variables from TF_VAR_-prefixed environment
# variables, and project_id / region have no defaults on either module — a
# defaulted project ID is how an apply lands in the wrong project. Exported
# here so the tf-* recipes below carry them; CI exports the same set itself.
TF_VAR_project_id      := $(PROJECT_ID)
TF_VAR_region          := $(REGION)
TF_VAR_hosting_site_id := $(HOSTING_SITE_ID)
export TF_VAR_project_id TF_VAR_region TF_VAR_hosting_site_id

ARTEFACTS_BUCKET      := $(PROJECT_ID)-model-artefacts
IMAGE_REPO            := $(REGION)-docker.pkg.dev/$(PROJECT_ID)/will-it-rain-images
IMAGE_NAME            := pipeline
IMAGE_TAG             ?= latest
BACKEND_IMAGE_NAME    := backend
BACKEND_IMAGE_TAG     ?= latest
BACKEND_SERVICE       := backend
BACKEND_GO_IMAGE_NAME := backend-go
BACKEND_GO_IMAGE_TAG  ?= latest
BACKEND_GO_SERVICE    := backend-go
PIPELINE_SPEC         := build/pipeline.yaml
IMAGE_SENTINEL        := build/.image-pushed
BACKEND_IMAGE_SENTINEL := build/.backend-image-pushed
BACKEND_GO_IMAGE_SENTINEL := build/.backend-go-image-pushed
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

# Files baked into the Go backend image. Tests are excluded deliberately:
# they are in the build context but never in the image, so a test-only edit
# would otherwise push a byte-identical binary under a fresh digest. The
# equivalent Python lists get this for free from the src/ vs tests/ split.
BACKEND_GO_IMAGE_SOURCES := backend-go/Dockerfile backend-go/.dockerignore \
			    backend-go/go.mod backend-go/go.sum \
			    $(shell find backend-go/cmd backend-go/internal \
					-name '*.go' -not -name '*_test.go')

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
	go-format-check go-vet go-test go-check go-format go-fix \
	check fix prek \
	backend-dev frontend-dev dev \
	image compile-pipeline upload-pipeline deploy-pipeline \
	trigger-pipeline-from-local trigger-pipeline-via-scheduler clean \
	backend-image backend-deploy \
	backend-go-image backend-go-deploy \
	model-refresher-source upload-model-refresher-source \
	golden-fixtures \
	frontend-build frontend-site-check frontend-deploy \
	tf-init tf-plan tf-apply

# ---------------------------------------------------------------------------
# Dev tooling
# ---------------------------------------------------------------------------
#
# Python tooling (ruff, ty, pytest) covers the uv workspace: pipeline, backend,
# shared library. Frontend tooling (biome, tsc) covers the TypeScript workspace
# under frontend/. Go tooling (gofmt, vet, go test) covers the module under
# backend-go/. `check` and `fix` aggregate all three.

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

# Go — read-only checks over the backend-go module. `go -C` keeps these
# runnable from the repo root, the same way `pnpm -C` does for the frontend.
#
# golangci-lint is deliberately absent: it is wired into the prek hooks and
# has its own CI job, and unlike ruff (which uv pins in the workspace) it is
# an unmanaged binary that `make check` would then require everyone to have
# installed. `make prek` is the recipe that runs it.
go-format-check: ## gofmt -s -l, no writes
	@unformatted=$$(gofmt -s -l backend-go); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt -s needed:"; echo "$$unformatted"; exit 1; \
	fi

go-vet: ## go vet (suspicious constructs)
	go -C backend-go vet ./...

go-test: ## go test across the module
	go -C backend-go test ./...

go-check: go-format-check go-vet go-test ## all Go read-only checks

# Go — auto-fixers (mutate the working tree). Matches the prek gofmt hook.
go-format: ## gofmt -s (writes)
	gofmt -s -w backend-go

go-fix: go-format ## all Go auto-fixers

# Aggregators across every toolchain. Default for "did I break anything?".
check: py-check frontend-check go-check ## all read-only checks (CI parity, all stacks)

fix: py-fix frontend-fix go-fix ## all auto-fixers (all stacks)

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
#
# PROJECT / LOCATION are what the app-side settings (and Cloud Run, and the
# Vertex SDK) call these; config.env uses the Makefile / TF_VAR_ names. Neither
# pair can rename without breaking the other side, so map them here — in
# deployment Terraform sets the app-side names directly and nothing maps.
backend-dev: ## reloading FastAPI server on $(BACKEND_DEV_PORT)
	PROJECT=$(PROJECT_ID) LOCATION=$(REGION) \
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
# (trigger-pipeline-from-local, deploy-pipeline) depend on it so they
# rebuild + push when any IMAGE_SOURCES file is newer than the sentinel.
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

# Build image + spec from the working tree, then submit a one-off pipeline
# run via the aiplatform SDK. Use this when iterating on pipeline code —
# the run reflects local edits, not what's deployed. Depends on both the
# compiled spec and a current image; without the latter, Vertex would pull
# a stale :latest and changes to component bodies (e.g. train.py) would
# silently fail to take effect.
trigger-pipeline-from-local: $(PIPELINE_SPEC) $(IMAGE_SENTINEL)
	uv run --package pipeline python -m pipeline.trigger

# Fire the Cloud Scheduler job that the weekly cron also fires. Runs the
# pipeline spec currently staged in GCS (gs://…-model-artefacts/pipelines/
# will-it-rain.yaml) — i.e. whatever CI last published from main — against
# the :latest pipeline image in Artifact Registry. Use this for off-cycle
# runs of the deployed pipeline; no local build happens.
trigger-pipeline-via-scheduler:
	gcloud scheduler jobs run will-it-rain-weekly-training \
	    --location=$(REGION)

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
# Go backend build / deploy
# ---------------------------------------------------------------------------

# Build and push the Go backend image. The Dockerfile cross-compiles to
# amd64 whatever it is built on, but the --platform flag is still what stops
# the *manifest* being tagged arm64 from an Apple Silicon machine — which
# Cloud Run rejects. Context is backend-go/, not the repo root: the Go module
# is self-contained, where backend/ needs the uv workspace above it.
backend-go-image: $(BACKEND_GO_IMAGE_SENTINEL)

$(BACKEND_GO_IMAGE_SENTINEL): $(BACKEND_GO_IMAGE_SOURCES)
	docker buildx build \
	    --platform linux/amd64 \
	    --push \
	    --tag $(IMAGE_REPO)/$(BACKEND_GO_IMAGE_NAME):$(BACKEND_GO_IMAGE_TAG) \
	    --file backend-go/Dockerfile \
	    backend-go
	@mkdir -p $(dir $@) && touch $@

# Roll out a new revision of the backend-go service. Same reasoning as
# `backend-deploy`: Cloud Run revisions pin a digest, so a pushed image does
# not reach traffic on its own.
#
# The service itself is Terraform's, and does not exist yet — until
# cloud_run_go.tf lands, `backend-go-image` is the useful half of this pair,
# and it is what gives that first apply a :latest tag to point at.
backend-go-deploy: $(BACKEND_GO_IMAGE_SENTINEL)
	gcloud run services update $(BACKEND_GO_SERVICE) \
	    --region=$(REGION) \
	    --image=$(IMAGE_REPO)/$(BACKEND_GO_IMAGE_NAME):$(BACKEND_GO_IMAGE_TAG)

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
# Cross-language test fixtures
# ---------------------------------------------------------------------------

# Generate what the Go backend's parity tests assert against: a locally
# trained model (model.txt, serving.json), one raw Open-Meteo FlatBuffers
# response (forecast.fb), and the outputs the Python serving path produces
# from the two (expected.json), written to backend-go/testdata and checked in.
#
# No GCP: the model is trained here over a fixed date window rather than
# downloaded from the registry, so this needs no credentials and isn't gated
# on a challenger beating the champion. See golden_fixtures/model.py for the
# full reasoning. Takes ~20s, mostly the historical fetch; reruns reproduce
# model.txt and serving.json byte-for-byte, while forecast.fb and the
# expected.json derived from it move with the live forecast.
#
# Kept out of `make help` because it rewrites checked-in fixtures — re-run it
# deliberately, when the serving contract changes.
golden-fixtures:
	uv run --package golden-fixtures python -m golden_fixtures

# ---------------------------------------------------------------------------
# Terraform
# ---------------------------------------------------------------------------

# Thin wrappers so terraform picks up TF_VAR_project_id / TF_VAR_region from
# config.env — neither module defaults them, so a raw `terraform -chdir=…`
# from an unexported shell just sits there prompting. -input=false makes that
# a clear error rather than a hang.
#
# infra/main is the default; bootstrap is run as the human user, per
# docs/cicd.md:
#     make tf-apply TF_MODULE=infra/bootstrap
TF_MODULE ?= infra/main

tf-init:
	terraform -chdir=$(TF_MODULE) init -input=false

tf-plan:
	terraform -chdir=$(TF_MODULE) plan -input=false

tf-apply:
	terraform -chdir=$(TF_MODULE) apply -input=false

# ---------------------------------------------------------------------------
# Frontend build / deploy
# ---------------------------------------------------------------------------

# Build the SPA into frontend/dist. Firebase Hosting deploy publishes from
# that directory (configured in frontend/firebase.json).
frontend-build:
	pnpm -C frontend install --frozen-lockfile
	pnpm -C frontend build

# frontend/firebase.json has to repeat HOSTING_SITE_ID — JSON can't
# interpolate, and the Firebase CLI takes the site from firebase.json only
# (no --site flag, no env override). Drift is silent rather than fatal: with
# no matching "site", the CLI falls back to the project's *default* site and
# happily publishes there, so the deploy "succeeds" against the wrong
# hostname. Fail here instead.
frontend-site-check:
	@json_site=$$(python3 -c 'import json; print(json.load(open("frontend/firebase.json"))["hosting"]["site"])'); \
	if [ "$$json_site" != "$(HOSTING_SITE_ID)" ]; then \
	    echo "frontend/firebase.json site '$$json_site' != HOSTING_SITE_ID '$(HOSTING_SITE_ID)' in config.env"; \
	    exit 1; \
	fi

# Publish the built SPA to Firebase Hosting. TF owns the site shape (project
# enrolment, site, /api/** rewrite to Cloud Run); this just ships content,
# mirroring the backend-deploy pattern. Requires `firebase login` once.
#
# --project rather than a .firebaserc default: that file was a second place
# naming the project, and one the CLI can rewrite behind your back (`firebase
# use` edits it).
frontend-deploy: frontend-site-check frontend-build
	cd frontend && firebase deploy --only hosting --project $(PROJECT_ID)
