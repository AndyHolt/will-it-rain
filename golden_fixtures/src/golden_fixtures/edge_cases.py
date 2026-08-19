"""Construct the cases a captured forecast cannot be relied on to contain.

Two behaviours matter to the Go port and neither is pinned by
``expected.json``:

*Missing features.* LightGBM sends NaN down a split's learned default
direction, so a missing value is not the same as a zero. The live payload does
happen to carry NaN — ``ukmo_uk_deterministic_2km__showers`` comes back empty,
taking 4 of the 70 features with it — but that is Open-Meteo's behaviour on
the day, not a property of the contract. If they ever populate the column the
coverage vanishes and no test goes red. So the NaN is placed here deliberately,
in named columns, at known offsets from the anchor: once in a base feature and
once in a lagged one, so a Go bug in either lookup is distinguishable.

*Isotonic calibration at its edges.* Evaluating the calibrator is linear
interpolation between knots, held flat beyond the outermost pair by
``out_of_bounds="clip"``. A trained model cannot be steered to a chosen raw
probability by constructing its inputs, so these are recorded as raw →
calibrated pairs straight from the fitted calibrator rather than as
predictions: on and past each end, across the steepest step, and along the
longest flat run.

The forecast frames are serialised as the canonical column map, not as
FlatBuffers. Building a payload would need the generated builders and would
re-test the parse path that ``expected.json`` already covers; what is under
test here starts one stage later, at feature assembly.
"""

import json
from pathlib import Path

import pandas as pd

from golden_fixtures.expected import TIMESTAMP_FORMAT, nullable, serialise_forecast
from will_it_rain_shared.features import build_features
from will_it_rain_shared.forecast import FORECAST_MODELS, FORECAST_VARIABLES
from will_it_rain_shared.predict import TrainedModel, predict_from_model

EDGE_CASES_FILENAME = "edge_cases.json"

# A fixed winter morning, chosen to differ from the captured payload in both
# `hour_of_day` and `month`: if Go transposed the two seasonal features, the
# captured fixture alone (21:00 in August) would not necessarily catch it.
_START = pd.Timestamp("2026-01-15T00:00:00Z")
_HOURS = 8

# The anchor sits far enough in for every lag to resolve, with rows after it,
# as in a real forecast window.
_ANCHOR_INDEX = 5

# Synthetic but plausible: a wet, overcast, breezy hour drifting slowly over
# the window. `(base, per-hour delta)` per variable, so no two hours share a
# value and a lag lookup that reads the wrong row is visible in the vector.
# Values are float64 here, unlike the float32 the FlatBuffers payload carries,
# because the Go side reads these from JSON as float64 too — both languages
# score the identical numbers.
_VARIABLE_SHAPE: dict[str, tuple[float, float]] = {
    "temperature_2m": (4.0, 0.25),
    "relative_humidity_2m": (88.0, -0.5),
    "apparent_temperature": (1.5, 0.3),
    "precipitation": (0.2, 0.1),
    "rain": (0.2, 0.1),
    "showers": (0.0, 0.05),
    "cloud_cover": (95.0, -1.0),
    "wind_speed_10m": (18.0, 0.4),
    "wind_direction_10m": (210.0, 2.0),
}

# The two models disagree slightly, as they do in a real response.
_MODEL_OFFSET: dict[str, float] = {
    "ukmo_uk_deterministic_2km": 0.0,
    "ecmwf_ifs": 0.35,
}

# `(name, description, [(hours before the anchor, column)])`. The columns are
# named explicitly rather than picked programmatically: which feature is
# missing is the substance of the case, and it should be readable from the
# fixture without running the generator.
_MISSING_VALUE_CASES: list[tuple[str, str, list[tuple[int, str]]]] = [
    (
        "all_present",
        "Control: no missing values, so the two cases below differ only by their NaN.",
        [],
    ),
    (
        "nan_in_base_feature",
        "NaN at the anchor hour, so the base feature is missing and its lags are not.",
        [(0, "ukmo_uk_deterministic_2km__precipitation")],
    ),
    (
        "nan_in_lagged_feature",
        "NaN two hours before the anchor, so only the __lag2h feature is missing.",
        [(2, "ecmwf_ifs__cloud_cover")],
    ),
]


def _build_forecast() -> pd.DataFrame:
    """Build the synthetic forecast frame every missing-value case starts from."""
    index = pd.date_range(start=_START, periods=_HOURS, freq="1h", name="date")
    columns = {}
    for model in FORECAST_MODELS:
        for variable in FORECAST_VARIABLES:
            # KeyError rather than a default: a variable added to the shared
            # list should fail here loudly, not enter the fixture as zeros.
            base, delta = _VARIABLE_SHAPE[variable]
            offset = _MODEL_OFFSET[model]
            columns[f"{model}__{variable}"] = [base + offset + delta * i for i in range(_HOURS)]
    return pd.DataFrame(columns, index=index)


def _prediction_case(
    trained_model: TrainedModel,
    name: str,
    description: str,
    missing: list[tuple[int, str]],
) -> dict[str, object]:
    """Score one constructed forecast and record its inputs and outputs."""
    forecast = _build_forecast()
    anchor_utc = forecast.index[_ANCHOR_INDEX]
    for hours_before, column in missing:
        forecast.loc[anchor_utc - pd.Timedelta(hours=hours_before), column] = float("nan")

    prediction = predict_from_model(trained_model, forecast, anchor_utc)
    trimmed = forecast.drop(
        columns=[c for c in trained_model.sparse_columns if c in forecast.columns]
    )
    features = build_features(trimmed, trained_model.lag_hours)
    row = features.loc[anchor_utc, trained_model.feature_cols]

    return {
        "name": name,
        "description": description,
        "forecast": serialise_forecast(forecast),
        "anchor_utc": anchor_utc.strftime(TIMESTAMP_FORMAT),
        "feature_vector": [nullable(v) for v in row],
        "raw_prob": prediction.raw_prob,
        "calibrated_prob": prediction.calibrated_prob,
        "will_rain": prediction.will_rain,
    }


def _calibration_cases(trained_model: TrainedModel) -> list[dict[str, object]]:
    """Sample the calibrator out of range, across its steepest step, and on a flat run.

    Isotonic regression fitted to 0/1 labels comes out as a staircase: long
    flat runs joined by segments a thousandth of a unit wide that climb by
    tenths. Interpolation is therefore where the arithmetic can go wrong by a
    lot — a small error in the position along the steepest segment is a large
    error in the output — so the segment is located by slope rather than by a
    fixed index, and stays the discriminating probe if the model is retrained.

    The out-of-range probes are weaker than they look, and deliberately kept
    anyway: this calibrator saturates at 0 and 1, so clipping and linear
    extrapolation agree beyond the outermost knots and no probe can separate
    them. What they do pin is that out-of-range input yields the boundary
    value at all, rather than a panic, a NaN, or a zero from an index that ran
    off the end.
    """
    x = [float(v) for v in trained_model.calibrator.X_thresholds_]
    y = [float(v) for v in trained_model.calibrator.y_thresholds_]
    segments = range(len(x) - 1)
    steepest = max(segments, key=lambda i: (y[i + 1] - y[i]) / (x[i + 1] - x[i]))
    flattest = max(segments, key=lambda i: (x[i + 1] - x[i]) if y[i + 1] == y[i] else 0.0)

    def within(segment: int, fraction: float) -> float:
        return x[segment] + fraction * (x[segment + 1] - x[segment])

    probes = [
        ("zero", 0.0),
        ("below_first_knot", x[0] / 2),
        ("first_knot", x[0]),
        ("quarter_across_steepest_step", within(steepest, 0.25)),
        ("half_across_steepest_step", within(steepest, 0.5)),
        ("three_quarters_across_steepest_step", within(steepest, 0.75)),
        ("mid_longest_flat_run", within(flattest, 0.5)),
        ("last_knot", x[-1]),
        ("above_last_knot", (x[-1] + 1.0) / 2),
        ("one", 1.0),
    ]
    calibrated = trained_model.calibrator.transform([raw for _, raw in probes])
    return [
        {"name": name, "raw_prob": raw, "calibrated_prob": float(value)}
        for (name, raw), value in zip(probes, calibrated, strict=True)
    ]


def write_edge_cases(trained_model: TrainedModel, directory: Path) -> tuple[int, int]:
    """Write ``edge_cases.json`` out; returns the count of cases of each kind."""
    prediction_cases = [
        _prediction_case(trained_model, name, description, missing)
        for name, description, missing in _MISSING_VALUE_CASES
    ]
    calibration_cases = _calibration_cases(trained_model)

    edge_cases = {
        "feature_cols": list(trained_model.feature_cols),
        "threshold": float(trained_model.threshold),
        "prediction_cases": prediction_cases,
        "calibration_cases": calibration_cases,
    }
    (directory / EDGE_CASES_FILENAME).write_text(
        json.dumps(edge_cases, indent=2, allow_nan=False) + "\n"
    )
    return len(prediction_cases), len(calibration_cases)
