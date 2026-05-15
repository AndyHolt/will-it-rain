# Base image for KFP pipeline components.
# Build context is the repo root, not pipeline/:
#   docker build -f pipeline/Dockerfile -t pipeline:latest .

FROM python:3.14-slim

# LightGBM needs libgomp1 (OpenMP runtime); not in slim by default.
RUN apt-get update \
    && apt-get install -y --no-install-recommends libgomp1 \
    && rm -rf /var/lib/apt/lists/*

# Pin uv from its official image — much faster than pip-installing it.
COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /bin/

WORKDIR /app

# Workspace root manifest + lockfile, plus the two packages this image needs.
COPY pyproject.toml uv.lock ./
COPY will_it_rain_shared/ ./will_it_rain_shared/
COPY pipeline/ ./pipeline/

# `--package pipeline` installs only pipeline's runtime deps (and its
# workspace dep on will_it_rain_shared). Backend and dev deps are excluded.
RUN uv sync --package pipeline --frozen --no-dev

ENV PATH="/app/.venv/bin:$PATH"
