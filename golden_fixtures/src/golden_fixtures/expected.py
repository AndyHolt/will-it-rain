"""Compute what the Go backend must reproduce, from the Python serving path.

Everything here is derived from the two fixtures already on disk — the model
that ``model.py`` trained and the payload ``capture.py`` captured — so the
expectations are pinned to the exact bytes Go reads, not to a second fetch
that might have moved.

Three layers are recorded, because a parity failure is far easier to place
when the innermost stage that disagrees is visible: the forecast parsed into
its canonical column map, the feature vector assembled at the anchor, and the
prediction itself. Scoring goes through ``shared.predict.predict_from_model``,
which is the function the Python backend served from. With that backend
deleted this package is its only caller: it survives as the reference the Go
scoring is checked against.

NaN is written as JSON ``null``: the JSON spec has no NaN literal, Python's
default ``NaN`` output is invalid JSON, and Go's ``encoding/json`` would
reject it. Readers on the Go side unmarshal the float fields as pointers (or
``json.Number``) and treat null as a missing value — which is what LightGBM
consumes it as anyway.
"""

import json
import math
from pathlib import Path

import pandas as pd
from openmeteo_sdk.WeatherApiResponse import WeatherApiResponse

from golden_fixtures.capture import FORECAST_FILENAME, PAST_HOURS
from will_it_rain_shared.features import build_features
from will_it_rain_shared.forecast import FORECAST_VARIABLES, model_to_name
from will_it_rain_shared.predict import (
    PREDICTION_WINDOW_HOURS,
    Prediction,
    TrainedModel,
    predict_from_model,
)

EXPECTED_FILENAME = "expected.json"

# Each message in the payload is preceded by its length as a little-endian
# uint32. A multi-model response is a *sequence* of these, so the reader
# advances by prefix + body rather than assuming a single root — the same
# framing the Go client has to implement.
_SIZE_PREFIX_BYTES = 4

# Timestamps go out in the shape Go's time.RFC3339 parses, with a literal Z
# rather than pandas' "+00:00", so the fixture reads the same in both
# languages. Public, with the helpers below, because the constructed fixture
# in `edge_cases` has to serialise to exactly the same conventions.
TIMESTAMP_FORMAT = "%Y-%m-%dT%H:%M:%SZ"


def read_forecast(directory: Path) -> pd.DataFrame:
    """Parse the captured payload into the canonical ``{model}__{variable}`` frame.

    The mapping came from the Python backend's ``forecast_fetch``, restated
    here rather than imported because that module fetched as it parsed. With
    the backend deleted this is no longer a mirror of a second implementation
    but the oracle itself: the canonical map it produces is what
    ``expected.json`` pins, and ``forecast.decode`` on the Go side is what
    that pin then checks. Unlike the scoring below, which stays genuine
    Python/Go parity through ``will_it_rain_shared``, a rule wrong in both
    places here would agree and pass.
    """
    payload = (directory / FORECAST_FILENAME).read_bytes()

    combined: pd.DataFrame | None = None
    position = 0
    while position < len(payload):
        length = int.from_bytes(
            payload[position : position + _SIZE_PREFIX_BYTES], byteorder="little"
        )
        response = WeatherApiResponse.GetRootAs(payload, position + _SIZE_PREFIX_BYTES)
        position += _SIZE_PREFIX_BYTES + length

        model_name = model_to_name(response.Model())
        hourly = response.Hourly()
        if hourly is None:
            raise RuntimeError(f"Captured payload for {model_name} had no hourly data.")
        data: dict[str, object] = {
            "date": pd.date_range(
                start=pd.to_datetime(hourly.Time(), unit="s", utc=True),
                end=pd.to_datetime(hourly.TimeEnd(), unit="s", utc=True),
                freq=pd.Timedelta(seconds=hourly.Interval()),
                inclusive="left",
            )
        }
        for i, variable_name in enumerate(FORECAST_VARIABLES):
            variable = hourly.Variables(i)
            if variable is None:
                raise RuntimeError(
                    f"Captured payload for {model_name} missing variable {variable_name}."
                )
            data[f"{model_name}__{variable_name}"] = variable.ValuesAsNumpy()
        frame = pd.DataFrame(data)
        combined = frame if combined is None else combined.merge(frame, on="date", how="outer")

    if combined is None:
        raise RuntimeError(f"{FORECAST_FILENAME} contained no FlatBuffers messages.")
    return combined.set_index("date").sort_index()


def pick_fixture_anchor(forecast: pd.DataFrame) -> tuple[pd.Timestamp, pd.Timestamp]:
    """Return the wall clock to serve at and the anchor hour it selects.

    A frozen payload has no "now", so one is reconstructed: the request asked
    for ``PAST_HOURS`` of history, which puts the hour the capture ran in at
    that offset. Half past it is a wall clock that exercises the flooring, so
    the Go test can feed ``now_utc`` to its own anchor selection and assert it
    lands on ``anchor_utc`` rather than trusting a hardcoded index.

    The rule — latest forecast hour at or before ``now`` floored to the hour —
    came from the Python backend's ``pick_anchor``, and is stated here for the
    same reason ``read_forecast`` states the column mapping: it is the oracle
    ``features.PickAnchor`` on the Go side is checked against.
    """
    now_utc = forecast.index[PAST_HOURS] + pd.Timedelta(minutes=30)
    candidates = forecast.index[forecast.index <= now_utc.floor("h")]
    if len(candidates) == 0:
        raise RuntimeError(f"Captured forecast has no rows at or before {now_utc}.")
    return now_utc, candidates.max()


def nullable(value: float) -> float | None:
    """Render NaN as JSON null; anything else as a plain float."""
    number = float(value)
    if math.isnan(number):
        return None
    if math.isinf(number):
        raise ValueError("Infinite value in fixture data — expected floats or NaN.")
    return number


def serialise_forecast(forecast: pd.DataFrame) -> dict[str, object]:
    """Render a forecast frame as the canonical column map the Go tests read."""
    return {
        "times_utc": [t.strftime(TIMESTAMP_FORMAT) for t in forecast.index],
        "columns": {str(name): [nullable(v) for v in values] for name, values in forecast.items()},
    }


def write_expected(trained_model: TrainedModel, directory: Path) -> Prediction:
    """Score the captured forecast and write ``expected.json`` out.

    Returns the prediction as well, so the caller can report it without
    reading the file back.
    """
    forecast = read_forecast(directory)
    now_utc, anchor_utc = pick_fixture_anchor(forecast)
    prediction = predict_from_model(trained_model, forecast, anchor_utc)

    # Recomputed rather than returned by predict_from_model, which yields only
    # the prediction. The two agree by construction: same drop, same builder,
    # same anchor.
    trimmed = forecast.drop(
        columns=[c for c in trained_model.sparse_columns if c in forecast.columns]
    )
    features = build_features(trimmed, trained_model.lag_hours)
    row = features.loc[anchor_utc, trained_model.feature_cols]

    expected = {
        "forecast": serialise_forecast(forecast),
        "now_utc": now_utc.strftime(TIMESTAMP_FORMAT),
        "anchor_utc": anchor_utc.strftime(TIMESTAMP_FORMAT),
        "window_end_utc": (anchor_utc + pd.Timedelta(hours=PREDICTION_WINDOW_HOURS)).strftime(
            TIMESTAMP_FORMAT
        ),
        "feature_cols": list(trained_model.feature_cols),
        "feature_vector": [nullable(v) for v in row],
        "raw_prob": prediction.raw_prob,
        "calibrated_prob": prediction.calibrated_prob,
        "threshold": prediction.threshold,
        "will_rain": prediction.will_rain,
    }
    # allow_nan=False so an unconverted NaN fails here rather than producing a
    # file the Go side cannot parse.
    (directory / EXPECTED_FILENAME).write_text(
        json.dumps(expected, indent=2, allow_nan=False) + "\n"
    )
    return prediction
