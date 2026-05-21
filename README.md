<img src="frontend/public/favicon.svg" alt="" width="64" height="64" align="left" />

# Will it rain?

Local rain forecasting for where I live. Uses historical weather forecasts along
with research-grade weather station data to get improved rain forecasting for my
home.

Because the forecast prediction is tied to the vicinity of the specific weather
station location, the app does not show the forecast for the user's location,
only for the static location of the weather station. This is useful to me,
because it is where I live.

## Motivation

The two weather forecasts I regularly check for short-term weather forecasts are
systematically over-optimistic and pessimistic. Apple Weather underpredicts
rain, while BBC weather overpredicts.

<figure align="center">
  <p align="center"><img src="docs/apple-forecast.png" alt="Apple Weather forecast showing a mostly dry week" width="45%" /> <img src="docs/bbc-forecast.png" alt="BBC Weather forecast showing rain on most days of the same week" width="45%" /></p>
  <figcaption align="center"><table align="center" width="80%"><tr><td align="center"><sub><em>Forecasts from Apple Weather (left) and BBC Weather (right)
  for the same time. Apple predicts dry, BBC predicts likely to rain. This is
  representative of what I have observed as systematic optimistic/pessimistic variation between the two.</em></sub></td></tr></table></figcaption>
</figure>
<br/>

Since these are regular variances, not completely random, it suggests that with
accurate local data for rainfall, a model can learn a more accurate prediction
from the two forecasts.

There happens to be a research-grade weather station a few hundred metres from
where I live, with open and accessible data. This gives a very good set of real
observations to train against.

Unfortunately, neither Apple nor the BBC publish their historic forecasts, so
without spending a few years collecting such forecast data in preparation, we
can't use the Apple and BBC forecasts themselves.

But the underlying models which I think they use are made available through
Open-Meteo. Apple's [Weather data
sources](https://support.apple.com/en-gb/105038) lists ECMWF and The Met Office,
while [BBC Weather](https://www.bbc.co.uk/weather) lists The Met Office as a
major source.

## Data sources

Two upstream APIs, both queried on-demand inside the weekly training pipeline:

- **[Open-Meteo historical-forecast API](https://open-meteo.com/en/docs/historical-forecast-api)** for hourly forecast features. Two
  models are fetched in parallel — the UK Met Office's
  `ukmo_uk_deterministic_2km` and ECMWF's `ecmwf_ifs`. Training uses the
  historical-forecast endpoint, which shows the one-hour ahead forecast for each
  hourly interval over the past several years.
- **[COSMOS UK](https://cosmos.ceh.ac.uk/)** research-grade weather station data
  via the CEH endpoint, pulling half-hourly pluvio precipitation readings from
  the site near my home. These observations are the ground truth that the model
  is trained to predict.

## Model and training

The target is binary: **will it rain in the next 4 hours?**, where "rain"
means cumulative precipitation of **≥ 0.1 mm** measured by the COSMOS pluvio
over the 4-hour window starting at the anchor hour. That same window and
threshold are used for the persistence and naive-forecast baselines that the
trained model has to beat.

Features at each anchor hour are the nine Open-Meteo variables from both
forecast models, plus lag-1/2/3-hour copies of each, plus `hour_of_day` and
`month`. The classifier is a LightGBM binary model with early stopping on a
held-out validation slice. Its raw probabilities are then run through an
isotonic regression calibrator fitted on the same validation slice, which
corrects magnitude drift between the (drier) training period and val/test,
and the decision threshold is chosen to maximise F1 on the calibrated val
predictions. Splits are chronological 70/15/15 over the full available
window — no shuffling, since temporal leakage would flatter the test score.

Training runs as a Vertex AI Pipeline every Sunday at 02:00 UTC. Each run
re-fetches data, retrains from scratch, and evaluates a challenger model
against the current production champion on the same held-out test set; the
`production` alias in the Model Registry only moves forward if the
challenger beats the champion by a margin.

<figure align="center">
  <p align="center"><img src="docs/pipeline-graph.png" alt="Vertex AI pipeline graph: fetch-forecast and fetch-observations feed prepare, then train, then evaluate, then a register-and-promote group containing register and promote." width="280" /></p>
  <figcaption align="center"><table align="center" width="80%"><tr><td align="center"><sub><em>Vertex AI training pipeline. Fetches historic forecasts and
  real observations, then prepares training data (features and labels) from
  these. Runs model training using the new data set, then evaluates the new
  model. The register and promote step is gated on the model performing better
  than the persistence baseline. If gate condition passes, the new model is
  registered, and promoted only if the new model (challenger) performs better
  than the existing production model (champion) on the same test data set.</em></sub></td></tr></table></figcaption>
</figure>
<br/>

## Frontend

When the site is loaded, it calls `/api/predict` once and renders a plain,
straightforward prediction of whether there will be rain or not in the next four
hours (a four hour window, starting at the most recent hour).

Additional details are displayed, such as calibrated rain probability and the
model's decision threshold.

## Technical challenges and design decisions

### Data availability

Since the data of past forecasts from Apple and the BBC are not available, we
can't use those directly. One option would be to collect hour-by-hour forecasts
until we have enough to train a model, but that would take a long time.

Since the aim isn't so much to learn the biases of the Apple and BBC forecasts,
but to get a more accurate forecast for one particular location, it doesn't
matter if the input data is the original forecasts I observed or upstream
models, so long as we can train a more accurate model for the specific location.

My biggest initial question to validate the whole project was whether training a
small model at a workable scale could possibly beat the enormous and complex
models that serve the main forecasts. The data available from Open-Meteo was
adequate for validating this question.

More exploration into available data, or collecting data specifically for the
project, may yield better models over time. But I wanted to get a model working
quickly and deployed, get the system in place to make it useful, and then
consider iterating the model and input data from there.

### Forecast granularity and matching availability for inference

The next challenge on the available data is matching what the historical
forecast data means with the data that's available at inference time.

The specific metric I want to predict is "given the current forecasts, what is
most likely in the next 4 hours". At time T, I want to predict what will happen
in the interval (T, T+4h). And the available data at time T are the current
forecasts for each hourly range: (T+1h, T+2h, T+3h, T+4h)

However, the Open-Meteo data does not allow looking back to what the forecast
was at, say, 11am on a particular date for the time 1pm. Instead, it gives the
hourly forecast for each our, using the model window. For hourly models, this
means each hour window gets its immediate single hour forecast. In other words,
over a four hour window, we can get (T+1h, (T+1)+1h, (T+2)+1h, (T+3)+1h).

At inference time, this would require being able to see 3 hours into the future,
to see what the forecast in 3 hours time will be for the 4th hour of the
interval.

So to make it an even playing field, I cut off forecasts at the "present"
moment to forecast over a 4 hour window. forecasts for previous time to T can be
used, but no forward looking forecasts.

This almost certainly reduces the quality of the model. But it keeps us honest
about the model predictive performance.

### Seasonality and threshold calibration

Splitting available window leads to training, validating and test windows
covering different seasons, with different total rainfall.

### Scheduling Vertex AI Pipeline using Terraform

Scheduling a Vertex Pipeline run using Terraform is not trivial.

Canonical reference seems to be Datatonic blog post:
https://datatonic.com/insights/vertex-ai-pipelines-terraform-cloud-scheduler/

Uses a Cloud Scheduler which runs weekly, sending HTTP request to create
pipeline job. Jobs are then "one off" runs.

Bit of a split in responsibilities between CI/CD uploading new YAML, and TF
running job which fetches pipeline config and runs it.

Alternative: set up schedule in console.

### Serving from new model after promotion

When a new model is promoted, the backend instance(s) serving the replaced model
need to switch over to the new model.

One way to handle this would be on every request to check that the model is up
to date, and load the new model if required. But this adds complexity and
latency to every request, to handle model updates that occur only very rarely.
Other approaches which require the backend service to manage model updates also
suffer from adding complexity to the backend service.

Instead, we can add a new component to the system that responds to a promotion
event by triggering a refresh of all deployed backend instances. When the
backend instances restart, they will load the updated model, and serve from the
new model for the rest of their lifespan. This keeps the concern completely out
of the backend, at the cost of moving the complexity elsewhere.

When the pipeline promotes a new model, a new pipeline component,
`publish_promotion_op` sends a message to a pub/sub topic. This triggers a cloud
run function, which marks backend instances as requiring refresh. Cloud Run will
then rotate the live instances, and fresh instances will have the new model.

This approach works well here because the cost of serving predictions from an
outdated model during the overlap period is very small. Model improvements are
expected to be incremental. So it will be better to quickly serve a prediction
from an outdated model than have to wait for a new model to be loaded before
serving the request.

The main downside of this approach is adding the extra moving part: the cloud
function that triggers the refresh in response to the pub/sub message. But this
is a reasonable price to pay to keep the pipeline and backend services simple
and without strong coupling.

See [PR #41](https://github.com/AndyHolt/will-it-rain/pull/41).
