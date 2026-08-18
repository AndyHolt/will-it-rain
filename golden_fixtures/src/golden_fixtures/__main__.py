"""Entry point behind ``make golden-fixtures``.

Run from the repo root: the fixture directory is a relative path. Needs no GCP
credentials — Open-Meteo and COSMOS UK are public — but does need the private
location config in ``.env`` (see ``.env-example``).

"""

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict

from golden_fixtures import FIXTURE_DIR
from golden_fixtures.capture import capture_forecast
from golden_fixtures.expected import write_expected
from golden_fixtures.model import TRAINING_END, TRAINING_START, train_serving_artefacts


class Settings(BaseSettings):
    # See pipeline/trigger.py for why required fields use `Field(...)`.
    LATITUDE: float = Field(...)
    LONGITUDE: float = Field(...)
    COSMOS_UK_SITE_CODE: str = Field(...)

    model_config = SettingsConfigDict(env_file=".env", extra="ignore")


def main() -> None:
    s = Settings()
    FIXTURE_DIR.mkdir(parents=True, exist_ok=True)

    print(f"Training fixture model on {TRAINING_START} to {TRAINING_END}...")
    trained_model = train_serving_artefacts(
        s.LATITUDE, s.LONGITUDE, s.COSMOS_UK_SITE_CODE, FIXTURE_DIR
    )
    payload_bytes = capture_forecast(s.LATITUDE, s.LONGITUDE, FIXTURE_DIR)
    prediction = write_expected(trained_model, FIXTURE_DIR)

    # What the fixtures actually contain is the first thing you want when a
    # parity test starts failing, and nothing else records it yet.
    print(f"Wrote fixtures to {FIXTURE_DIR}:")
    print(
        f"  model.txt      {trained_model.model.booster_.num_trees()} trees, "
        f"{len(trained_model.feature_cols)} features"
    )
    print(
        f"  serving.json   threshold {trained_model.threshold:.3f}, "
        f"sparse {trained_model.sparse_columns}"
    )
    print(f"  forecast.fb    {payload_bytes} bytes")
    print(
        f"  expected.json  anchor {prediction.anchor_utc:%Y-%m-%dT%H:%MZ}, "
        f"calibrated {prediction.calibrated_prob:.4f}, "
        f"will_rain {prediction.will_rain}"
    )


if __name__ == "__main__":
    main()
