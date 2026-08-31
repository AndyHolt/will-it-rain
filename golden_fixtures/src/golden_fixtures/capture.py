"""Capture one live Open-Meteo response as the raw FlatBuffers payload.

Captured live rather than synthesised, because a hand-written forecast would
only pin what we already believe to be true. The point is to freeze bytes the
Go code has never seen, with whatever ragged edges the two forecast models
happened to have that hour — the all-NaN column in a live response is exactly
the kind of thing nobody writes into a fixture by hand.

Fetched as raw FlatBuffers rather than through ``openmeteo_requests`` so the
file is the wire payload byte-for-byte: Go parses the same bytes Python does,
and the JSON endpoint's quantisation (integers for humidity and wind
direction, 1dp for temperature) never enters the picture.
"""

from pathlib import Path

from niquests import RetryConfiguration, Session

from will_it_rain_shared.forecast import (
    CURRENT_FORECAST_BASE_URL,
    FORECAST_MODELS,
    FORECAST_VARIABLES,
)

FORECAST_FILENAME = "forecast.fb"

# Must match the window the service requests — `defaultPastHours` and
# `defaultForecastHours` in backend/internal/forecast. A fixture fetched
# over a different window would not exercise the same lags.
PAST_HOURS = 24
FORECAST_HOURS = 24

# The live forecast endpoint returns a few KB and is not request-cached the way
# the historical endpoint is, so the long read timeout used there isn't needed.
_CONNECT_TIMEOUT_SECONDS = 10
_READ_TIMEOUT_SECONDS = 30

# Same reasoning as pipeline/components/fetch_forecast.py: urllib3 retries
# transport errors by default but not HTTP failures.
_RETRIES = RetryConfiguration(
    total=5,
    backoff_factor=2.0,
    backoff_jitter=1.0,
    status_forcelist=(429, 500, 502, 503, 504),
)


def capture_forecast(latitude: float, longitude: float, directory: Path) -> int:
    """Fetch one live forecast as FlatBuffers and write the raw payload out.

    A multi-model response is a *sequence* of size-prefixed messages, not one
    root, so the file is stored whole and framing is left to the readers.
    Returns the payload size in bytes.
    """
    session = Session(retries=_RETRIES)
    response = session.get(
        CURRENT_FORECAST_BASE_URL,
        params={
            "latitude": str(latitude),
            "longitude": str(longitude),
            "past_hours": str(PAST_HOURS),
            "forecast_hours": str(FORECAST_HOURS),
            "hourly": ",".join(FORECAST_VARIABLES),
            "models": ",".join(FORECAST_MODELS),
            "format": "flatbuffers",
        },
        timeout=(_CONNECT_TIMEOUT_SECONDS, _READ_TIMEOUT_SECONDS),
    )
    response.raise_for_status()
    payload = response.content
    if not payload:
        raise RuntimeError("Open-Meteo returned an empty body for the forecast request.")
    (directory / FORECAST_FILENAME).write_bytes(payload)
    return len(payload)
