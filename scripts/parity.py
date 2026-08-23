# /// script
# requires-python = ">=3.14"
# ///
"""Compare the two backends' answers field by field, during the Go migration.

Run from the repo root against the deployed services, before the Firebase
Hosting rewrite is flipped to `backend-go`:

    make parity                        # one sample
    make parity PARITY_ARGS="--samples 3"

Both services resolve the same `@production` champion and score it from the
same Open-Meteo forecast, so their `/api/predict` bodies should be identical
rather than merely close. The tolerance is there for float formatting, not
for modelling differences: everything a reimplementation can get subtly wrong
— a feature in the wrong column, LightGBM's default direction on a NaN, an
isotonic segment off by one — moves a probability far more than 1e-9.

**A mismatch is not automatically a bug in the Go service.** Each service
caches its own forecast for ten minutes and refetches on its own schedule, so
predictions made either side of an Open-Meteo refresh differ for a reason
that has nothing to do with the port. Two things narrow that: the requests go
out concurrently, so the window between them is milliseconds rather than
seconds, and `--samples` re-checks across the cache lifetime. A difference
that survives samples spanning more than ten minutes is a real one.

Needs gcloud authenticated (to resolve the service URLs) but no application
credentials — both services are public.
"""

import argparse
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from typing import Any

# Compared exactly: two strings and a bool, where any difference at all is a
# difference in what the user is shown.
PREDICT_EXACT = ("anchor_utc", "window_end_utc", "will_rain", "model_version")

# Compared against the tolerance. All three are in [0, 1], so an absolute
# comparison is the meaningful one.
PREDICT_NUMERIC = ("raw_prob", "calibrated_prob", "threshold")

# `loaded_at_utc` is deliberately absent: each service loaded the champion
# when its own instance started, and those times have no reason to agree.
HEALTH_EXACT = ("status", "model_version", "model_resource")

# Comfortably past the 600s forecast cache in both services, so consecutive
# samples score fetches rather than one cached copy.
DEFAULT_INTERVAL = 630.0

# A cold Python backend takes ~35s to answer (which is the whole reason for
# the port), and this script is often the first thing to hit it in a while.
DEFAULT_TIMEOUT = 90.0


def service_url(project: str, region: str, service: str) -> str:
    """Ask Cloud Run where a service answers."""
    command = [
        "gcloud",
        "run",
        "services",
        "describe",
        service,
        "--project",
        project,
        "--region",
        region,
        "--format",
        "value(status.url)",
    ]
    result = subprocess.run(command, capture_output=True, text=True)
    if result.returncode != 0:
        raise SystemExit(f"resolving {service}: {result.stderr.strip()}")
    url = result.stdout.strip()
    if not url:
        raise SystemExit(f"resolving {service}: Cloud Run reported no URL")
    return url


def get_json(url: str, timeout: float) -> dict[str, Any]:
    """GET a JSON body, surfacing an error response as its own message.

    A 503 from `/api/predict` carries the reason in `detail`, and losing that
    to a bare HTTPError is losing the answer to why parity could not be
    checked.
    """
    try:
        with urllib.request.urlopen(url, timeout=timeout) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        body = error.read().decode("utf-8", errors="replace").strip()
        raise SystemExit(f"GET {url}: HTTP {error.code}: {body}") from error
    except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as error:
        raise SystemExit(f"GET {url}: {error}") from error


def fetch_both(blue: str, green: str, path: str, timeout: float) -> tuple[dict, dict]:
    """GET `path` from both services at once.

    Concurrently, and not for speed. The two answers are comparable only if
    they were made in the same hour from the same forecast, and every second
    between the requests is a second in which either could change.
    """
    with ThreadPoolExecutor(max_workers=2) as pool:
        futures = [pool.submit(get_json, url + path, timeout) for url in (blue, green)]
        return futures[0].result(), futures[1].result()


def differences(
    blue: dict[str, Any],
    green: dict[str, Any],
    exact: tuple[str, ...],
    numeric: tuple[str, ...],
    tolerance: float,
) -> list[str]:
    """Describe every way the two bodies disagree, or return an empty list."""
    diffs = []

    # A field one service answers with and the other does not is a contract
    # difference the frontend would see, whatever the shared fields say.
    if only_blue := sorted(blue.keys() - green.keys()):
        diffs.append(f"fields only in blue: {', '.join(only_blue)}")
    if only_green := sorted(green.keys() - blue.keys()):
        diffs.append(f"fields only in green: {', '.join(only_green)}")

    for field in exact:
        left, right = blue.get(field), green.get(field)
        if left != right:
            diffs.append(f"{field}: {left!r} vs {right!r}")

    for field in numeric:
        left, right = blue.get(field), green.get(field)
        if not isinstance(left, (int, float)) or not isinstance(right, (int, float)):
            diffs.append(f"{field}: {left!r} vs {right!r} (not both numbers)")
        elif abs(left - right) > tolerance:
            diffs.append(f"{field}: {left!r} vs {right!r} (differ by {abs(left - right):.3g})")

    return diffs


def take_sample(blue: str, green: str, timeout: float, tolerance: float) -> list[str]:
    """Compare one pair of predictions, retrying once if the hour rolls.

    An `anchor_utc` mismatch between two concurrent requests means the clock
    crossed the hour between them, not that the services disagree — the
    prediction is for a different hour, so nothing else in the body is
    comparable either. Rare, and cured by asking again.
    """
    for attempt in range(2):
        blue_body, green_body = fetch_both(blue, green, "/api/predict", timeout)
        if blue_body.get("anchor_utc") == green_body.get("anchor_utc"):
            print(
                f"    anchor {blue_body.get('anchor_utc')}"
                f"  calibrated {blue_body.get('calibrated_prob')}"
            )
            return differences(blue_body, green_body, PREDICT_EXACT, PREDICT_NUMERIC, tolerance)
        if attempt == 0:
            print("    the hour rolled between the two requests; asking again")

    return [
        f"anchor_utc: {blue_body.get('anchor_utc')!r} vs {green_body.get('anchor_utc')!r} "
        "(twice — not a clock roll)"
    ]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Compare the deployed backend and backend-go predictions."
    )
    parser.add_argument("--project", default=os.environ.get("PROJECT_ID"))
    parser.add_argument("--region", default=os.environ.get("REGION"))
    parser.add_argument("--blue", default="backend", help="the incumbent service")
    parser.add_argument("--green", default="backend-go", help="the candidate service")
    parser.add_argument("--blue-url", help="skip gcloud and use this URL")
    parser.add_argument("--green-url", help="skip gcloud and use this URL")
    parser.add_argument(
        "--samples", type=int, default=1, help="predictions to compare, spaced by --interval"
    )
    parser.add_argument(
        "--interval",
        type=float,
        default=DEFAULT_INTERVAL,
        help=f"seconds between samples (default {DEFAULT_INTERVAL:.0f}, past both caches)",
    )
    parser.add_argument("--tolerance", type=float, default=1e-9)
    parser.add_argument("--timeout", type=float, default=DEFAULT_TIMEOUT)
    args = parser.parse_args()

    for side in ("blue", "green"):
        if not getattr(args, f"{side}_url") and not (args.project and args.region):
            parser.error(
                f"--{side}-url, or --project and --region (make parity exports both "
                "from config.env)"
            )
    return args


def main() -> int:
    args = parse_args()
    blue = args.blue_url or service_url(args.project, args.region, args.blue)
    green = args.green_url or service_url(args.project, args.region, args.green)
    print(f"blue   {blue}")
    print(f"green  {green}")

    # Health first. Two services on different champions disagree on every
    # probability, and no prediction body says which champion produced it
    # beyond a version string that would then be the only clue.
    blue_health, green_health = fetch_both(blue, green, "/api/health", args.timeout)
    if health_diffs := differences(blue_health, green_health, HEALTH_EXACT, (), args.tolerance):
        print("\n/api/health disagrees, so the predictions are not comparable:")
        for diff in health_diffs:
            print(f"    {diff}")
        return 1
    print(f"both serving model version {blue_health.get('model_version')}\n")

    failed = 0
    for sample in range(1, args.samples + 1):
        if sample > 1:
            print(f"  waiting {args.interval:.0f}s before the next sample")
            time.sleep(args.interval)
        print(f"  sample {sample}/{args.samples}")
        if diffs := take_sample(blue, green, args.timeout, args.tolerance):
            failed += 1
            for diff in diffs:
                print(f"    MISMATCH {diff}")

    if failed:
        print(f"\nparity FAILED: {failed} of {args.samples} samples disagree")
        return 1
    print(f"\nparity holds: {args.samples} sample(s) identical within {args.tolerance:g}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
