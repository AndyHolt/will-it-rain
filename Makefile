# Repo-wide recipes: dev tooling (lint/format/typecheck/test) + pipeline
# build/compile/upload/trigger.
#
# Run from the repo root. Pipeline-side targets also assume:
#   - gcloud is authenticated and `gcloud auth configure-docker
#     europe-west2-docker.pkg.dev` has been run once.
#   - Docker (with buildx) is running locally.
#   - uv has the workspace synced.

PROJECT_ID       := will-it-rain-496215
REGION           := europe-west2
ARTEFACTS_BUCKET := $(PROJECT_ID)-model-artefacts
IMAGE_REPO       := $(REGION)-docker.pkg.dev/$(PROJECT_ID)/will-it-rain-images
IMAGE_NAME       := pipeline
IMAGE_TAG        ?= latest
PIPELINE_SPEC    := build/pipeline.yaml
IMAGE_SENTINEL   := build/.image-pushed

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

.DEFAULT_GOAL := help

.PHONY: help \
        lint format-check typecheck test check \
        lint-fix format fix prek \
        image compile-pipeline upload-pipeline deploy-pipeline trigger-run clean

# ---------------------------------------------------------------------------
# Dev tooling
# ---------------------------------------------------------------------------

# Print available targets (anything whose recipe line carries a `## doc`).
help:
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# Read-only checks (no mutations) — what CI runs.
lint: ## ruff lint, no fixes
	uv run ruff check

format-check: ## ruff format --check, no writes
	uv run ruff format --check

typecheck: ## ty static type checks
	uv run ty check

test: ## pytest across the workspace
	uv run pytest

check: lint format-check typecheck test ## all read-only checks (CI parity)

# Auto-fixers — mutate the working tree.
lint-fix: ## ruff lint with --fix
	uv run ruff check --fix

format: ## ruff format (writes)
	uv run ruff format

fix: lint-fix format ## all auto-fixers

# Run the full prek hook set against every file. Matches what pre-commit
# enforces locally and what the prek hook job runs in CI.
prek: ## prek run --all-files
	prek run --all-files

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

clean:
	rm -rf build/
