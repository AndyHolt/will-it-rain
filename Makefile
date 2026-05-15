# Pipeline build / compile / upload recipes.
#
# Run from the repo root. Assumes:
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

.PHONY: image compile-pipeline upload-pipeline deploy-pipeline trigger-run clean

# Build and push the pipeline component image. Vertex AI workers are x86_64;
# building on Apple Silicon without --platform produces an arm64 image that
# fails on the Vertex side with "exec format error". buildx + --platform
# cross-compiles cleanly, and --push uploads directly (no local load step,
# which doesn't work for cross-platform builds anyway).
image:
	docker buildx build \
	    --platform linux/amd64 \
	    --push \
	    --tag $(IMAGE_REPO)/$(IMAGE_NAME):$(IMAGE_TAG) \
	    --file pipeline/Dockerfile \
	    .

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

clean:
	rm -rf build/
