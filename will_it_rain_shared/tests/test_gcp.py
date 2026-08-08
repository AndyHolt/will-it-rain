import importlib
import sys
from pathlib import Path

import pytest

MODULE = "will_it_rain_shared.gcp"
REPO_ROOT = Path(__file__).parents[2]


@pytest.fixture
def fresh_import(monkeypatch):
    """Import will_it_rain_shared.gcp with a controlled environment and cwd.

    The module resolves its names at import time, so each case needs a real
    re-import rather than the copy sys.modules may already be holding. The
    cwd matters because the config.env path is relative.
    """

    def _import(cwd: Path, **env: str):
        monkeypatch.delenv("PROJECT_ID", raising=False)
        monkeypatch.delenv("REGION", raising=False)
        for name, value in env.items():
            monkeypatch.setenv(name, value)
        monkeypatch.chdir(cwd)
        monkeypatch.delitem(sys.modules, MODULE, raising=False)
        return importlib.import_module(MODULE)

    return _import


def test_environment_overrides_the_file_and_names_derive_from_it(fresh_import):
    gcp = fresh_import(REPO_ROOT, PROJECT_ID="some-project", REGION="some-region")

    assert gcp.PROJECT_ID == "some-project"
    assert gcp.ARTEFACTS_BUCKET == "some-project-model-artefacts"
    assert gcp.PIPELINE_SERVICE_ACCOUNT == "pipeline@some-project.iam.gserviceaccount.com"
    assert gcp.IMAGE_REPO == "some-region-docker.pkg.dev/some-project/will-it-rain-images"


def test_reads_config_env_without_anything_exported(fresh_import):
    """The point of the env_file: entry points work outside `make` too."""
    gcp = fresh_import(REPO_ROOT)

    assert gcp.PROJECT_ID
    assert gcp.REGION
    assert gcp.ARTEFACTS_BUCKET == f"{gcp.PROJECT_ID}-model-artefacts"


def test_without_config_env_or_environment_the_error_names_config_env(fresh_import, tmp_path):
    with pytest.raises(RuntimeError, match="config.env"):
        fresh_import(tmp_path)
