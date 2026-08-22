// Package server exposes the prediction service over HTTP.
//
// Two routes: /api/predict scores the current forecast against the loaded
// model, /api/health reports which model that is.
//
// The model is loaded once at startup and never replaced. A promotion rolls
// the service instead (see model_refresher/), so an instance serves exactly
// one version for its whole life — which is what keeps the request path free
// of locking.
package server
