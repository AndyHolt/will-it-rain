"""Train the fixture model locally, from public data, with no GCP involved."""

from datetime import date
from pathlib import Path

from pipeline.components.fetch_forecast import fetch_forecast
from pipeline.components.fetch_observations import fetch_observations
from pipeline.components.prepare import prepare
from pipeline.components.train import save_serving_artefacts, train
from will_it_rain_shared.predict import TrainedModel

# Absolute, not relative to today: regenerating the fixtures should pull the
# same data and produce the same model, so a parity failure means the code
# moved. One full calendar year gives the `month` feature every value it can
# take, and trains in seconds.
TRAINING_START = date(2024, 1, 1)
TRAINING_END = date(2024, 12, 31)


def train_serving_artefacts(
    latitude: float,
    longitude: float,
    site_code: str,
    directory: Path,
) -> TrainedModel:
    """Train a model over the fixed fixture window and write its serving artefacts.

    Runs the same ``fetch → prepare → train`` path as the pipeline, calling the
    same code as the pipeline components.

    Returns the ``TrainedModel`` as well as writing it out, because the
    expected outputs are computed from the in-memory estimator via the shared
    predict path.
    """
    forecast = fetch_forecast(latitude, longitude, TRAINING_START, TRAINING_END)
    observations = fetch_observations(site_code, TRAINING_START, TRAINING_END)
    prepared = prepare(forecast, observations)
    trained_model = train(prepared)
    save_serving_artefacts(trained_model, directory)
    return trained_model
