"""Evaluate the challenger against baselines and (when available) the champion."""

from dataclasses import dataclass
from typing import Any

import numpy as np
import pandas as pd
from sklearn.metrics import f1_score, precision_score, recall_score

from pipeline.components.prepare import PreparedData
from pipeline.components.train import TrainedModel

PERSISTENCE_WINDOW_HOURS = 4
PRECIP_BASELINE_COLUMN = "ukmo_uk_deterministic_2km__precipitation"
PRECIP_BASELINE_THRESHOLD_MM = 0.1


@dataclass(frozen=True)
class EvalMetrics:
    f1: float
    precision: float
    recall: float
    predicted_positive_rate: float
    actual_positive_rate: float


@dataclass(frozen=True)
class BaselineMetrics:
    persistence: EvalMetrics
    precipitation_threshold: EvalMetrics


@dataclass(frozen=True)
class EvaluationResult:
    challenger: EvalMetrics
    baselines: BaselineMetrics
    champion: EvalMetrics | None
    test_start: pd.Timestamp
    test_end: pd.Timestamp
    n_test_rows: int


def _metrics(y_true: np.ndarray, y_pred: np.ndarray) -> EvalMetrics:
    y_true_arr = np.asarray(y_true).astype(bool)
    y_pred_arr = np.asarray(y_pred).astype(bool)
    return EvalMetrics(
        f1=float(f1_score(y_true_arr, y_pred_arr, zero_division=0.0)),
        precision=float(precision_score(y_true_arr, y_pred_arr, zero_division=0.0)),
        recall=float(recall_score(y_true_arr, y_pred_arr, zero_division=0.0)),
        predicted_positive_rate=float(y_pred_arr.mean()) if len(y_pred_arr) else 0.0,
        actual_positive_rate=float(y_true_arr.mean()) if len(y_true_arr) else 0.0,
    )


def _score_bundle(test_df: pd.DataFrame, bundle: dict[str, Any]) -> EvalMetrics:
    """Score a model bundle (challenger or champion) on the test frame."""
    feature_cols: list[str] = list(bundle["feature_cols"])
    missing = [c for c in feature_cols if c not in test_df.columns]
    if missing:
        raise RuntimeError(f"Bundle expects feature columns not present in test set: {missing}")
    X = test_df[feature_cols]
    y_true = test_df["will_rain"].astype(int).to_numpy()
    raw = bundle["model"].predict_proba(X)[:, 1]
    calibrated = bundle["calibrator"].transform(raw)
    y_pred = calibrated >= bundle["threshold"]
    return _metrics(y_true, y_pred)


def _evaluate_persistence(test_df: pd.DataFrame) -> EvalMetrics:
    """Persistence baseline: predict rain in [T, T+4h) by what happened in
    [T-4h, T). Implemented as label-shift-forward by ``PERSISTENCE_WINDOW_HOURS``.

    The first few test rows have no in-sample history to look back on (the
    shift produces NaN), and are excluded from the metric — a small loss on
    a large test set.
    """
    actual = test_df["will_rain"].astype(bool)
    predicted = test_df["will_rain"].shift(PERSISTENCE_WINDOW_HOURS)
    eligible = predicted.notna()
    return _metrics(actual[eligible].to_numpy(), predicted[eligible].astype(bool).to_numpy())


def _evaluate_precip_threshold(test_df: pd.DataFrame) -> EvalMetrics:
    """Naive forecast baseline: rain iff ``ukmo_uk_deterministic_2km__precipitation`` ≥ 0.1mm at T."""
    y_true = test_df["will_rain"].astype(bool).to_numpy()
    y_pred = (test_df[PRECIP_BASELINE_COLUMN] >= PRECIP_BASELINE_THRESHOLD_MM).to_numpy()
    return _metrics(y_true, y_pred)


def evaluate(
    prepared: PreparedData,
    trained: TrainedModel,
    champion_bundle: dict[str, Any] | None = None,
) -> EvaluationResult:
    """Score challenger, baselines, and (if provided) champion on the same test set.

    ``champion_bundle`` is the joblib-loaded dict from the current ``production``
    Model Registry entry, or ``None`` on the very first run when no production
    alias exists. The champion is scored on the same test rows as the challenger
    for a fair head-to-head comparison.
    """
    test = prepared.test

    challenger_bundle: dict[str, Any] = {
        "model": trained.model,
        "calibrator": trained.calibrator,
        "threshold": trained.threshold,
        "feature_cols": list(trained.feature_cols),
    }
    challenger = _score_bundle(test, challenger_bundle)
    persistence = _evaluate_persistence(test)
    precipitation_threshold = _evaluate_precip_threshold(test)
    champion = _score_bundle(test, champion_bundle) if champion_bundle is not None else None

    return EvaluationResult(
        challenger=challenger,
        baselines=BaselineMetrics(
            persistence=persistence,
            precipitation_threshold=precipitation_threshold,
        ),
        champion=champion,
        test_start=test.index[0],
        test_end=test.index[-1],
        n_test_rows=len(test),
    )
