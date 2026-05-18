# Cloud Run image for the FastAPI backend.
# Build context is the repo root, not backend/:
#   docker build -f backend/Dockerfile -t backend:latest .

FROM python:3.14-slim

# LightGBM needs libgomp1 (OpenMP runtime); not in slim by default.
# Required even for inference because joblib loads the C extension on import.
RUN apt-get update \
    && apt-get install -y --no-install-recommends libgomp1 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /bin/

WORKDIR /app

COPY pyproject.toml uv.lock ./
COPY will_it_rain_shared/ ./will_it_rain_shared/
COPY backend/ ./backend/

# `--package backend` installs only backend's runtime deps (and its
# workspace dep on will_it_rain_shared). Pipeline and dev deps are excluded.
RUN uv sync --package backend --frozen --no-dev

ENV PATH="/app/.venv/bin:$PATH"

# Cloud Run sets $PORT (defaults to 8080); honour it via the shell form.
ENV PORT=8080
EXPOSE 8080
CMD exec uvicorn backend.main:app --host 0.0.0.0 --port "$PORT"
