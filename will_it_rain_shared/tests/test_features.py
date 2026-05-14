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
        "hour_of_day",
        "month",
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


def test_hour_and_month_match_index():
    forecast = _sample_forecast()
    features = build_features(forecast)

    index = forecast.index
    assert isinstance(index, pd.DatetimeIndex)
    timestamps = index.to_series()
    assert (features["hour_of_day"] == timestamps.dt.hour).all()
    assert (features["month"] == timestamps.dt.month).all()


def test_input_dataframe_is_not_mutated():
    forecast = _sample_forecast()
    original = forecast.copy()
    build_features(forecast)
    pd.testing.assert_frame_equal(forecast, original)
