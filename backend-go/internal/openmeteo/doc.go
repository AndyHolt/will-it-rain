// Package openmeteo contains the FlatBuffers bindings for the Open-Meteo
// weather API response format.
//
// Open-Meteo publishes no Go bindings, so these are generated from its schema
// and checked in: flatc is needed to regenerate them, not to build.
//
// The schema version is pinned to the same release the Python side resolves
// (openmeteo-sdk 1.26.0 in uv.lock), because that is what produced the
// captured testdata/forecast.fb payload and the training-time forecast frames.
// Regenerate with:
//
//	curl -sfLO https://raw.githubusercontent.com/open-meteo/sdk/v1.26.0/flatbuffers/weather_api.fbs
//	flatc --go --go-namespace openmeteo -o internal/ weather_api.fbs
//
// Every other file in this package is generated output; edit the schema
// version above and rerun rather than editing them.
package openmeteo
