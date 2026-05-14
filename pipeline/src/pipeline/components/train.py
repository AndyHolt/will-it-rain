"""Train a LightGBM classifier with isotonic calibration."""

from collections.abc import Sequence
from dataclasses import dataclass, fields
from pathlib import Path

import joblib
import lightgbm as lgb
import numpy as np
from sklearn.isotonic import IsotonicRegression
from sklearn.metrics import f1_score

from pipeline.components.prepare import PreparedData

RANDOM_SEED = 42


@dataclass(frozen=True)
class TrainedModel:
    model: lgb.LGBMClassifier
    calibrator: IsotonicRegression
    threshold: float
    feature_cols: list[str]
    lag_hours: Sequence[int]
    sparse_columns: Sequence[str]


def train(
    prepared: PreparedData,
    *,
    random_seed: int = RANDOM_SEED,
    n_estimators: int = 1000,
    learning_rate: float = 0.05,
    num_leaves: int = 31,
    early_stopping_rounds: int = 50,
    threshold_grid_points: int = 91,
) -> TrainedModel:
    """Fit the classifier, calibrate on val, and pick an F1-max threshold.

    Calibration corrects the magnitude drift caused by the train regime being
    drier than val/test; with calibrated probabilities a magnitude threshold
    becomes meaningful. The threshold is then F1-maximised on the same
    calibrated val predictions.
    """
    X_train = prepared.train[prepared.feature_cols]
    y_train = prepared.train["will_rain"].astype(int)
    X_val = prepared.val[prepared.feature_cols]
    y_val = prepared.val["will_rain"].astype(int)

    model = lgb.LGBMClassifier(
        objective="binary",
        n_estimators=n_estimators,
        learning_rate=learning_rate,
        num_leaves=num_leaves,
        random_state=random_seed,
        verbose=-1,
    )
    model.fit(
        X_train,
        y_train,
        eval_set=[(X_val, y_val)],
        callbacks=[lgb.early_stopping(stopping_rounds=early_stopping_rounds, verbose=False)],
    )

    val_probs_raw = model.predict_proba(X_val)[:, 1]
    calibrator = IsotonicRegression(out_of_bounds="clip")
    calibrator.fit(val_probs_raw, y_val)
    val_probs = calibrator.transform(val_probs_raw)

    candidate_thresholds = np.linspace(0.05, 0.95, threshold_grid_points)
    val_f1s = [f1_score(y_val, val_probs >= t) for t in candidate_thresholds]
    best_threshold = float(candidate_thresholds[int(np.argmax(val_f1s))])

    return TrainedModel(
        model=model,
        calibrator=calibrator,
        threshold=best_threshold,
        feature_cols=prepared.feature_cols,
        lag_hours=prepared.lag_hours,
        sparse_columns=prepared.sparse_columns,
    )


def save_bundle(trained: TrainedModel, path: str | Path) -> None:
    """Serialise a TrainedModel to a joblib file at ``path``.

    Bundle keys mirror the dataclass field names; ``dataclasses.fields`` is used
    rather than ``asdict`` so the contained model/calibrator are stored by
    reference rather than deepcopied (which would be slow and can corrupt
    estimator state).
    """
    bundle = {f.name: getattr(trained, f.name) for f in fields(trained)}
    joblib.dump(bundle, path)
