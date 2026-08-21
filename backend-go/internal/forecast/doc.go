// Package forecast fetches the live hourly forecast from Open-Meteo.
//
// Uses FlatBuffers rather than JSON because the JSON endpoint quantises —
// integers for humidity and wind direction, 1dp for temperature. Training
// frames came through FlatBuffers, so JSON here would feed the model coarser
// values than it was fitted on.
//
// Client.Fetch returns a Forecast: hourly UTC timestamps and one
// "{model}__{variable}" column per pair. That column shape is the contract
// with the training-time fetcher, and is what internal/features assembles a
// vector from.
package forecast
