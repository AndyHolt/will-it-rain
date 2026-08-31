# Cold start

Both Cloud Run services scale to zero, so the latency of the first request
after an idle period is a headline number rather than a tail case. This is how
to measure it and what it currently is.

## Why it matters here

`min_instance_count = 0` (`infra/main/cloud_run.tf`, `cloud_run_go.tf`) is what
keeps hosting near-free, and traffic is around a dozen requests a week. At that
rate almost every request *is* the first after an idle period: the cold start is
the common case, not the exception. Cutting it from ~34s to under half a second
is the whole reason the backend was ported from Python to Go.

## Two numbers, two questions

- **`run.googleapis.com/container/startup_latencies`** — how long the platform
  took to bring an instance to ready, as Cloud Run itself measures it. This is
  the number the port set out to move, and the one that compares cleanly
  between the two services.
- **First-request wall time** — what a visitor waits for: instance startup,
  plus the request's own work (an Open-Meteo fetch against an empty forecast
  cache, ~0.3s), plus network from wherever you are measuring.

Quote the first; sanity-check it against the second.

## Measuring

**1. Let the service go idle.** Cloud Run reaps idle instances after roughly 15
minutes and nothing forces it sooner. Deploying a new revision does not help:
Cloud Run starts an instance to health-check the deploy, so a freshly deployed
revision is warm. Leave it 15+ minutes with no traffic — and note the public
site fetches `/api/predict` on page load, so a visitor resets the clock.

**2. Time one request.** Service URLs carry a per-project hash; `gcloud run
services list` prints them.

```
curl -s -o /dev/null \
    -w 'total=%{time_total}s ttfb=%{time_starttransfer}s\n' \
    https://backend-icxxlsgqsa-nw.a.run.app/api/predict
```

**3. Read Cloud Run's own measurement back.** There is no `gcloud` read surface
for time series — `gcloud monitoring time-series list` does not exist — so this
is a raw REST call. The `date` invocations below are BSD (macOS); GNU is
`date -u -d '1 hour ago'`.

```
curl -s -H "Authorization: Bearer $(gcloud auth print-access-token)" \
  -G "https://monitoring.googleapis.com/v3/projects/will-it-rain-496308/timeSeries" \
  --data-urlencode 'filter=metric.type="run.googleapis.com/container/startup_latencies"
                           AND resource.labels.service_name="backend"' \
  --data-urlencode "interval.startTime=$(date -u -v-1H +%Y-%m-%dT%H:%M:%SZ)" \
  --data-urlencode "interval.endTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  | jq -r '.timeSeries[].points[] | select(.value.distributionValue.count != null)
           | "\(.interval.endTime)  mean=\(.value.distributionValue.mean)ms"'
```

(The filter is shown wrapped for the page; send it on one line.) Points land a
minute or two behind the request, and each is a distribution — usually of a
single sample, since cold starts here do not arrive in batches.

## Results

Every cold start Cloud Run recorded in the 14 days to 2026-08-23. The Go
service has only had three, because it has only existed for a day:

| service      | runtime      |   n | median | range           |
|--------------|--------------|-----|--------|-----------------|
| `backend`    | Python 3.14  |  28 | 33.8s  | 24.1s – 38.9s   |
| `backend-go` | Go 1.26      |   3 | 0.45s  | 0.41s – 0.45s   |

These are the two services as they stood during the migration. `backend-go` has
since been destroyed and `backend` now runs the Go image, so the row that
describes today's service is the second one.

A **75x** reduction, and the spread collapses with it: the Python service
varied by 15 seconds between cold starts, the Go one by 40 milliseconds.

The comparison is not confounded by machine size: both services run 1 vCPU with
`startup_cpu_boost` and `cpu_idle`. They differ only in memory (1Gi vs 512Mi),
which startup latency does not depend on.

End to end, from a UK laptop, each after 16.5 minutes of idle:

| requested            | total  | connect |
|----------------------|--------|---------|
| 2026-08-23T21:11:41Z | 0.771s | 0.073s  |
| 2026-08-23T21:54:25Z | 0.980s | 0.059s  |

That is the ~0.45s of startup, plus an Open-Meteo round trip against an empty
forecast cache, plus the network from here — so a visitor's worst case is
around a second, against the ~35s it was.

Where the Python service's ~34s went, from `PYTHONPROFILEIMPORTTIME=1` on
revision `backend-00008-67s`:

| Phase                                            | Duration  |
|--------------------------------------------------|-----------|
| container start → Python running                 | 0.22s     |
| **module imports**                               | **26.4s** |
| lifespan (`Model.list` + GCS `.joblib` download) | 2.9s      |
| request work                                     | 0.34s     |

`google.cloud.aiplatform` was 13.5s of those imports on its own, `scipy` 3.2s,
`pandas` 2.0s. The Go service resolves the same champion over the same two
APIs and downloads the same artefacts; what it does not do is import 26s of
Python first.

## The cutover gate

Two conditions had to hold before Firebase Hosting was pointed at the Go
service: prediction parity within `1e-9`, and cold start under ~1s. Both held,
and the cutover is done — the `backend` service serves the Go image and the
green `backend-go` service it crossed on has been destroyed.

Parity was answered by `make parity` / `scripts/parity.py`, which compared the
two deployed services field by field. Both are deleted with the second service;
`git log` has them if the comparison is ever wanted again. The Go module's
golden-fixture tests (`backend/testdata`) still pin train/serve parity, which
is the part that outlives the migration. This document answers the second
condition.
