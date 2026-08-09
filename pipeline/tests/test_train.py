import json

import lightgbm as lgb
import numpy as np
import pandas as pd
import pytest

from pipeline.components.prepare import PreparedData
from pipeline.components.train import (
    SERVING_METADATA_FILENAME,
    SERVING_MODEL_FILENAME,
    save_serving_artefacts,
    train,
)
from will_it_rain_shared.predict import PREDICTION_WINDOW_HOURS, TrainedModel

FEATURE_COLS = [
    "best_match__precipitation",
    "best_match__precipitation__lag1h",
    "ecmwf_ifs__temperature_2m",
    "hour_of_day",
]


def _prepared_data() -> PreparedData:
    """A small, deterministic dataset with the shape `prepare` produces.

    The label is a noisy function of precipitation: signal enough for the
    booster to split on, noise enough that the isotonic calibrator fits more
    than one knot. `ecmwf_ifs__temperature_2m` carries NaNs, as the real
    outer merge across two forecast models does.
    """
    rng = np.random.default_rng(0)
    n = 240
    index = pd.date_range("2026-01-01", periods=n, freq="1h", tz="UTC")

    precipitation = rng.gamma(shape=0.5, scale=0.6, size=n)
    temperature = rng.normal(8.0, 3.0, size=n)
    temperature[rng.random(n) < 0.2] = np.nan

    wet = precipitation + rng.normal(0.0, 0.15, size=n) >= 0.3
    dataset = pd.DataFrame(
        {
            "best_match__precipitation": precipitation,
            "best_match__precipitation__lag1h": pd.Series(precipitation, index=index).shift(1),
            "ecmwf_ifs__temperature_2m": temperature,
            # `.dt.hour` via to_series() for the same reason build_features does:
            # `DatetimeIndex.hour` isn't in pandas-stubs.
            "hour_of_day": index.to_series().dt.hour,
            "will_rain": wet,
        },
        index=index,
    )

    return PreparedData(
        train=dataset.iloc[:144],
        val=dataset.iloc[144:192],
        test=dataset.iloc[192:],
        feature_cols=FEATURE_COLS,
        lag_hours=(1, 2, 3),
        sparse_columns=("ecmwf_ifs__showers",),
    )


@pytest.fixture(scope="module")
def trained_model() -> TrainedModel:
    return train(_prepared_data(), n_estimators=25, early_stopping_rounds=5)


@pytest.fixture(scope="module")
def serving_dir(trained_model: TrainedModel, tmp_path_factory: pytest.TempPathFactory):
    # Nested under a directory that does not exist, so the mkdir is covered:
    # `serving.path` from KFP is a declared output path, not a made one.
    directory = tmp_path_factory.mktemp("artefacts") / "serving"
    save_serving_artefacts(trained_model, directory)
    return directory


@pytest.fixture(scope="module")
def metadata(serving_dir) -> dict:
    return json.loads((serving_dir / SERVING_METADATA_FILENAME).read_text())


def test_writes_both_serving_files(serving_dir):
    assert (serving_dir / SERVING_MODEL_FILENAME).is_file()
    assert (serving_dir / SERVING_METADATA_FILENAME).is_file()


def test_native_model_scores_identically_to_the_bundle(serving_dir, trained_model: TrainedModel):
    """The native text model must reproduce `predict_proba`, NaNs included.

    This is the parity that a non-Python runtime depends on: it reads
    `model.txt` and has no access to the sklearn wrapper the bundle carries.
    """
    booster = lgb.Booster(model_file=str(serving_dir / SERVING_MODEL_FILENAME))
    rows = _prepared_data().test[FEATURE_COLS]
    assert rows["ecmwf_ifs__temperature_2m"].isna().any()

    expected = trained_model.model.predict_proba(rows)[:, 1]
    # `Booster.predict` is typed as a union covering sparse and list returns.
    np.testing.assert_allclose(np.asarray(booster.predict(rows)), expected, rtol=0, atol=1e-12)


def test_metadata_matches_the_trained_model(metadata: dict, trained_model: TrainedModel):
    assert metadata["feature_cols"] == trained_model.feature_cols
    assert metadata["lag_hours"] == list(trained_model.lag_hours)
    assert metadata["sparse_columns"] == list(trained_model.sparse_columns)
    assert metadata["threshold"] == pytest.approx(trained_model.threshold)
    assert metadata["prediction_window_hours"] == PREDICTION_WINDOW_HOURS


def test_metadata_holds_plain_json_scalars(metadata: dict):
    """numpy scalars survive `json.dumps` as bare numbers but read back oddly
    typed; assert the exact types a strict parser on the other side expects."""
    assert all(isinstance(c, str) for c in metadata["feature_cols"])
    assert all(isinstance(h, int) for h in metadata["lag_hours"])
    assert isinstance(metadata["threshold"], float)
    assert isinstance(metadata["prediction_window_hours"], int)


def test_isotonic_knots_interpolate_to_the_calibrator(metadata: dict, trained_model: TrainedModel):
    """Clamped linear interpolation over the knots == `calibrator.transform`.

    The knots are the whole calibrator as far as serving is concerned, so this
    pins the reduction. The probes deliberately run outside the knot range in
    both directions, which is where `out_of_bounds="clip"` shows up.
    """
    x = metadata["isotonic"]["x"]
    y = metadata["isotonic"]["y"]
    assert len(x) == len(y) >= 2

    probes = np.concatenate([[0.0, x[0] - 0.1], np.linspace(0.0, 1.0, 101), [x[-1] + 0.1, 1.0]])
    interpolated = np.interp(probes, x, y)  # np.interp clamps outside the range

    expected = trained_model.calibrator.transform(probes)
    np.testing.assert_allclose(interpolated, expected, rtol=0, atol=1e-12)
