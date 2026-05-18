"""Join forecast + observations, label, build features, split train/val/test."""

from collections.abc import Sequence
from dataclasses import dataclass

import pandas as pd

from will_it_rain_shared.features import DEFAULT_LAG_HOURS, build_features

DEFAULT_SPARSE_COLUMNS: tuple[str, ...] = ("ecmwf_ifs__showers",)


@dataclass(frozen=True)
class PreparedData:
    train: pd.DataFrame
    val: pd.DataFrame
    test: pd.DataFrame
    feature_cols: list[str]
    lag_hours: Sequence[int]
    sparse_columns: Sequence[str]


def build_labels(
    observations: pd.DataFrame,
    window_hours: int = 4,
    rain_threshold_mm: float = 0.1,
) -> pd.DataFrame:
    """Label hourly anchors as rain/no-rain over a forward window.

    ``observations`` is the half-hourly pluvio frame returned by
    ``fetch_observations``. Returns a frame with the same hourly UTC index,
    ``max_hourly_mm`` (forward window max of hourly totals) and ``will_rain``
    (bool, max ≥ ``rain_threshold_mm``). The last ``window_hours - 1`` rows
    are dropped because their forward window is incomplete.
    """
    hourly = observations["pluvio"].resample("1h").sum()
    forward_max = pd.concat(
        [hourly.shift(-i) for i in range(window_hours)],
        axis=1,
    ).max(axis=1)

    labels = pd.DataFrame(
        {"max_hourly_mm": forward_max, "will_rain": forward_max >= rain_threshold_mm}
    ).dropna()

    if window_hours > 1:
        labels = labels.iloc[: -(window_hours - 1)]
    return labels


def prepare(
    forecast: pd.DataFrame,
    observations: pd.DataFrame,
    sparse_columns: Sequence[str] = DEFAULT_SPARSE_COLUMNS,
    lag_hours: Sequence[int] = DEFAULT_LAG_HOURS,
    train_frac: float = 0.60,
    val_frac: float = 0.20,
    window_hours: int = 4,
    rain_threshold_mm: float = 0.1,
) -> PreparedData:
    """Build the training dataset and split chronologically into train/val/test.

    Sparse forecast columns are dropped (their values are mostly NaN for older
    rows and would create a distribution shift across the time-ordered splits).
    LightGBM tolerates remaining NaNs natively, so rows are only dropped when
    the label is missing.
    """
    forecast = forecast.drop(columns=[c for c in sparse_columns if c in forecast.columns])

    labels = build_labels(observations, window_hours, rain_threshold_mm)
    features = build_features(forecast, lag_hours)

    dataset = features.join(labels["will_rain"], how="inner")
    dataset = dataset[dataset["will_rain"].notna()]

    n = len(dataset)
    train_end = int(n * train_frac)
    val_end = int(n * (train_frac + val_frac))

    feature_cols = [c for c in dataset.columns if c != "will_rain"]
    return PreparedData(
        train=dataset.iloc[:train_end],
        val=dataset.iloc[train_end:val_end],
        test=dataset.iloc[val_end:],
        feature_cols=feature_cols,
        lag_hours=lag_hours,
        sparse_columns=sparse_columns,
    )
