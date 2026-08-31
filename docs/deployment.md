# Deployment

Procedures for shipping each component. The gotchas that bite are summarised
in `CLAUDE.md`; this file is the step-by-step.

Every command below reads the target project and region from `config.env`.
Terraform goes through `make tf-plan` / `make tf-apply` rather than
`terraform -chdir=…`: the modules have no defaults for `project_id`, `region`
or `hosting_site_id`, and the recipes are what export them.

## Pipeline trigger

Cloud Scheduler fires `will-it-rain-weekly-training` every Sunday 02:00 UTC,
POSTing to Vertex's `pipelineJobs.create` REST endpoint. The pipeline spec is
inlined into the request body by `infra/main/scheduler.tf`, which reads the
compiled YAML from `gs://…-model-artefacts/pipelines/will-it-rain.yaml` at
plan time.

Dev flow for off-cycle runs:

```
make trigger-pipeline-from-local       # build image + spec from working tree, submit via local SDK
make trigger-pipeline-via-scheduler    # fire the scheduled path against the spec staged in GCS
```

`trigger-pipeline-from-local` is for iterating on pipeline code;
`trigger-pipeline-via-scheduler` runs the spec currently staged in GCS, which
only moves forward when CI (or `make upload-pipeline` + `terraform apply`)
publishes it.

**Never switch the scheduler to `templateUri`** — Vertex's `templateUri` code
path mis-parses KFP 2.x YAML. Keep the inline `pipelineSpec` body.

## Backend

Cloud Run never auto-rolls on a new image push: each revision is pinned to a
digest at deploy time. The image must exist before TF can create the service,
and after that every code change needs an explicit redeploy.

First-time bootstrap (order matters):

```
make backend-go-image                  # builds + pushes :latest to AR
make tf-apply                          # creates the Cloud Run service
```

Subsequent code changes:

```
make backend-go-image                  # rebuild + push :latest
make backend-deploy                    # gcloud run services update → new revision
```

`terraform apply` owns service shape (env vars, scaling, SA, IAM); the
`:latest` tag stays put in TF state, so `apply` and `backend-deploy` don't
fight. `gcloud run services update` does flap `client`/`client_version` on the
resource — cosmetic drift only; ignore it on `terraform plan`.

Model promotion rolls the service automatically — see `model_refresher/main.py`,
whose module docstring explains why a forced revision beats a hot reload.

## Frontend

Same split as the backend: TF owns the shape (Firebase project enrolment,
Hosting site, `/api/**` rewrite to the Cloud Run backend in
`frontend/firebase.json`); the Firebase CLI ships content.

First-time bootstrap:

```
make tf-apply                          # enrols project with Firebase, creates site
firebase login                         # one-off, on the dev machine
make frontend-deploy                   # builds dist/ and runs `firebase deploy --only hosting`
```

Subsequent code changes: `make frontend-deploy` does both steps.

The Hosting site id comes from `HOSTING_SITE_ID` in `config.env`, and is
deliberately independent of the project id so the public URL doesn't move when
the project does. `frontend/firebase.json` has to repeat it — the Firebase CLI
takes the site from that file only — and `frontend-deploy` refuses to run if
the two disagree, because the CLI's own behaviour on a mismatch is to publish
to the project's default site without complaining.

The Hosting rewrite in `firebase.json` targets the Cloud Run service by **name**
(`backend` in `europe-west2`), not URL — so same-origin `/api/*` requests need
no CORS configuration. The backend is currently public (`allUsers` invoker); the
TODO in `cloud_run.tf` covers locking it down behind Firebase Hosting.
