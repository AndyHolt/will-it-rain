import numpy as np
import pandas as pd

from will_it_rain_shared.features import build_features


def _sample_forecast() -> pd.DataFrame:
    index = pd.date_range("2026-01-01", periods=8, freq="1h", tz="UTC")
    return pd.DataFrame(
        {
            "best_match__temperature_2m": np.arange(8, dtype=float),
            "best_match__precipitation": np.arange(8, dtype=float) * 0.1,
        },
        index=index,
    )


def test_build_features_adds_lags_and_calendar_columns():
    forecast = _sample_forecast()
    features = build_features(forecast, lag_hours=(1, 2))

    expected_columns = {
        "best_match__temperature_2m",
        "best_match__precipitation",
        "best_match__temperature_2m__lag1h",
        "best_match__precipitation__lag1h",
        "best_match__temperature_2m__lag2h",
        "best_match__precipitation__lag2h",
        "hour_of_day_sin",
        "hour_of_day_cos",
        "month_sin",
        "month_cos",
    }
    assert set(features.columns) == expected_columns


def test_lag_values_match_shifted_inputs():
    forecast = _sample_forecast()
    features = build_features(forecast, lag_hours=(1,))

    expected = forecast["best_match__temperature_2m"].shift(1)
    pd.testing.assert_series_equal(
        features["best_match__temperature_2m__lag1h"].rename("best_match__temperature_2m"),
        expected,
        check_names=False,
    )


def test_cyclical_encoding_matches_index():
    forecast = _sample_forecast()
    features = build_features(forecast)

    index = forecast.index
    assert isinstance(index, pd.DatetimeIndex)
    timestamps = index.to_series()
    expected_hour_angle = 2 * np.pi * timestamps.dt.hour / 24
    expected_month_angle = 2 * np.pi * (timestamps.dt.month - 1) / 12
    np.testing.assert_allclose(features["hour_of_day_sin"], np.sin(expected_hour_angle))
    np.testing.assert_allclose(features["hour_of_day_cos"], np.cos(expected_hour_angle))
    np.testing.assert_allclose(features["month_sin"], np.sin(expected_month_angle))
    np.testing.assert_allclose(features["month_cos"], np.cos(expected_month_angle))


def test_cyclical_encoding_wraps_at_boundaries():
    # Hour 23 should be adjacent to hour 0, and December (12) to January (1):
    # the cyclical encoding's whole point is that the wrap-around distance is
    # small. Verify by checking sin/cos pairs at the boundary positions.
    index = pd.DatetimeIndex(
        [
            "2026-01-01T00:00:00Z",  # hour 0, month 1
            "2026-01-01T23:00:00Z",  # hour 23, month 1
            "2026-12-15T12:00:00Z",  # hour 12, month 12
        ]
    )
    forecast = pd.DataFrame({"x": [0.0, 0.0, 0.0]}, index=index)
    features = build_features(forecast, lag_hours=())

    # Hour 0 and hour 23 differ by 1/24 of a full turn, so the angle gap is
    # 2π/24 ≈ 0.262 rad and the chord length is 2·sin(π/24) ≈ 0.261.
    h0 = np.array([features["hour_of_day_sin"].iloc[0], features["hour_of_day_cos"].iloc[0]])
    h23 = np.array([features["hour_of_day_sin"].iloc[1], features["hour_of_day_cos"].iloc[1]])
    assert np.linalg.norm(h0 - h23) < 0.3

    # January and December differ by 1/12 of a full turn; chord ≈ 0.518.
    jan = np.array([features["month_sin"].iloc[0], features["month_cos"].iloc[0]])
    dec = np.array([features["month_sin"].iloc[2], features["month_cos"].iloc[2]])
    assert np.linalg.norm(jan - dec) < 0.6


def test_input_dataframe_is_not_mutated():
    forecast = _sample_forecast()
    original = forecast.copy()
    build_features(forecast)
    pd.testing.assert_frame_equal(forecast, original)
