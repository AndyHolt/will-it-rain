package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AndyHolt/will-it-rain/backend/internal/forecast"
)

// tolerance is the parity budget against Python, as in internal/predict: the
// two walk the same trees and interpolate the same knots, so what is allowed
// for is float64 summation order.
const tolerance = 1e-9

// stubFetcher stands in for *forecast.Client, so the handler tests exercise
// the request path without an Open-Meteo round trip.
type stubFetcher struct {
	forecast *forecast.Forecast
	err      error
}

func (s stubFetcher) Fetch(context.Context) (*forecast.Forecast, error) {
	return s.forecast, s.err
}

// newTestServer wires a Server with the clock pinned, since the anchor — and
// so every value downstream of it — is chosen against it.
func newTestServer(model *Model, fetcher Fetcher, now time.Time) *Server {
	server := New(model, fetcher, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.now = func() time.Time { return now }
	return server
}

// get makes one request against the real mux, so routing and method matching
// are covered by every case rather than by a test of their own.
func get(t *testing.T, server *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, server, http.MethodGet, path)
}

func do(t *testing.T, server *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
	return recorder
}

// decodeInto decodes a recorded body, first checking the status and content
// type — a failure there explains a decode failure that would otherwise read
// as malformed JSON.
func decodeInto(t *testing.T, recorder *httptest.ResponseRecorder, status int, payload any) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d (body: %s)", recorder.Code, status, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != jsonContentType {
		t.Errorf("Content-Type = %q, want %q", got, jsonContentType)
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), payload); err != nil {
		t.Fatalf("decoding %s: %v", recorder.Body.String(), err)
	}
}

// TestPredictMatchesPython is what this package exists to pass: the same
// artefacts and the same captured forecast, served over HTTP, reaching the
// response the Python backend produced from them.
//
// The stages each have their own parity test in internal/features and
// internal/predict. What this adds is the composition — anchor, window,
// threshold and version arriving together in one body.
func TestPredictMatchesPython(t *testing.T) {
	expected := readExpected(t)
	server := newTestServer(
		loadGoldenModel(t, time.Now()),
		stubFetcher{forecast: expected.Forecast.build(t)},
		parseTime(t, expected.NowUTC),
	)

	var got PredictResponse
	decodeInto(t, get(t, server, "/api/predict"), http.StatusOK, &got)

	if want := parseTime(t, expected.AnchorUTC); !got.AnchorUTC.Equal(want) {
		t.Errorf("anchor_utc = %s, want %s", got.AnchorUTC, want)
	}
	if want := parseTime(t, expected.WindowEndUTC); !got.WindowEndUTC.Equal(want) {
		t.Errorf("window_end_utc = %s, want %s", got.WindowEndUTC, want)
	}
	if math.Abs(got.RawProb-expected.RawProb) > tolerance {
		t.Errorf("raw_prob = %.17g, want %.17g", got.RawProb, expected.RawProb)
	}
	if math.Abs(got.CalibratedProb-expected.CalibratedProb) > tolerance {
		t.Errorf("calibrated_prob = %.17g, want %.17g", got.CalibratedProb, expected.CalibratedProb)
	}
	// The threshold is copied from serving.json rather than computed, so an
	// exact comparison is the honest one.
	if got.Threshold != expected.Threshold {
		t.Errorf("threshold = %.17g, want %.17g", got.Threshold, expected.Threshold)
	}
	if got.WillRain != expected.WillRain {
		t.Errorf("will_rain = %t, want %t", got.WillRain, expected.WillRain)
	}
	if got.ModelVersion != fixtureVersionID {
		t.Errorf("model_version = %q, want %q", got.ModelVersion, fixtureVersionID)
	}
}

// TestPredictBodyMatchesThePythonShape pins the wire format rather than the
// values: the frontend is typed against these seven names
// (frontend/src/forecast/fetch.ts), and the timestamps against pydantic's
// rendering of an aware UTC datetime, which is what it will parse.
//
// Comparing the raw body is the point. Decoding into PredictResponse would
// assert the struct against itself and pass just as happily with a renamed
// field.
func TestPredictBodyMatchesThePythonShape(t *testing.T) {
	expected := readExpected(t)
	server := newTestServer(
		loadGoldenModel(t, time.Now()),
		stubFetcher{forecast: expected.Forecast.build(t)},
		parseTime(t, expected.NowUTC),
	)

	var body map[string]json.RawMessage
	decodeInto(t, get(t, server, "/api/predict"), http.StatusOK, &body)

	wantKeys := []string{
		"anchor_utc", "window_end_utc", "raw_prob", "calibrated_prob",
		"threshold", "will_rain", "model_version",
	}
	for _, key := range wantKeys {
		if _, ok := body[key]; !ok {
			t.Errorf("response has no %q field", key)
		}
	}
	if len(body) != len(wantKeys) {
		t.Errorf("response has %d fields, want %d: %s", len(body), len(wantKeys), body)
	}

	// pydantic renders an aware UTC datetime with a Z, not a +00:00 offset,
	// and the frontend parses whichever this sends.
	if got := string(body["anchor_utc"]); got != `"`+expected.AnchorUTC+`"` {
		t.Errorf("anchor_utc = %s, want %q", got, expected.AnchorUTC)
	}
}

// TestHealthReportsTheLoadedModel covers the fields the model refresher and a
// deploy check read to confirm which version an instance came up on.
func TestHealthReportsTheLoadedModel(t *testing.T) {
	loadedAt := time.Date(2026, 8, 19, 20, 15, 0, 0, time.UTC)
	server := newTestServer(loadGoldenModel(t, loadedAt), stubFetcher{}, loadedAt)

	var got HealthResponse
	decodeInto(t, get(t, server, "/api/health"), http.StatusOK, &got)

	if got.Status != "ok" {
		t.Errorf("status = %q, want \"ok\"", got.Status)
	}
	if got.ModelVersion == nil || *got.ModelVersion != fixtureVersionID {
		t.Errorf("model_version = %v, want %q", got.ModelVersion, fixtureVersionID)
	}
	if got.ModelResource == nil || *got.ModelResource != fixtureResourceName {
		t.Errorf("model_resource = %v, want %q", got.ModelResource, fixtureResourceName)
	}
	if got.LoadedAtUTC == nil || !got.LoadedAtUTC.Equal(loadedAt) {
		t.Errorf("loaded_at_utc = %v, want %s", got.LoadedAtUTC, loadedAt)
	}
}

// TestHealthWithoutAModelAnswersOK is the case health exists for: an instance
// that started before anything was promoted still answers, and says what it is
// missing. A 503 here would leave nothing to distinguish it from a service
// that is down.
func TestHealthWithoutAModelAnswersOK(t *testing.T) {
	server := newTestServer(nil, stubFetcher{}, time.Now())

	recorder := get(t, server, "/api/health")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	// Compared as a body rather than as a decoded struct: the three nulls are
	// the assertion, and a decode cannot tell a null from an absent field.
	want := `{"status":"ok","model_version":null,"model_resource":null,"loaded_at_utc":null}`
	if got := recorder.Body.String(); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

// TestPredictWithoutAModelIs503 pins the status and the detail text the Python
// backend answers with, which is what the frontend shows.
func TestPredictWithoutAModelIs503(t *testing.T) {
	server := newTestServer(nil, stubFetcher{}, time.Now())

	var got errorResponse
	decodeInto(t, get(t, server, "/api/predict"), http.StatusServiceUnavailable, &got)
	if got.Detail != noModelDetail {
		t.Errorf("detail = %q, want %q", got.Detail, noModelDetail)
	}
}

// TestPredictWhenTheForecastFailsIs500 also pins that the cause stays in the
// logs: the fetch error carries the request URL and whatever Open-Meteo
// answered, and this endpoint is public.
func TestPredictWhenTheForecastFailsIs500(t *testing.T) {
	secret := "open-meteo said no, at https://api.open-meteo.com/v1/forecast?latitude=55.9"
	server := newTestServer(
		loadGoldenModel(t, time.Now()),
		stubFetcher{err: errors.New(secret)},
		time.Now(),
	)

	recorder := get(t, server, "/api/predict")

	var got errorResponse
	decodeInto(t, recorder, http.StatusInternalServerError, &got)
	if got.Detail != forecastDetail {
		t.Errorf("detail = %q, want %q", got.Detail, forecastDetail)
	}
	if body := recorder.Body.String(); len(body) > len(forecastDetail)+len(`{"detail":""}`) {
		t.Errorf("body %s looks like it quotes the cause", body)
	}
}

// TestPredictWhenThePredictionFailsIs500 drives the second failure the handler
// can meet: a forecast that arrives but cannot be predicted from. Here it
// covers no hour at or before the clock, which PickAnchor rejects.
func TestPredictWhenThePredictionFailsIs500(t *testing.T) {
	expected := readExpected(t)
	server := newTestServer(
		loadGoldenModel(t, time.Now()),
		stubFetcher{forecast: expected.Forecast.build(t)},
		parseTime(t, expected.NowUTC).Add(-30*24*time.Hour),
	)

	var got errorResponse
	decodeInto(t, get(t, server, "/api/predict"), http.StatusInternalServerError, &got)
	if got.Detail != predictDetail {
		t.Errorf("detail = %q, want %q", got.Detail, predictDetail)
	}
}

// TestRoutingRejectsWhatIsNotServed keeps every response from this service one
// shape, including the ones the mux answers on its own account.
func TestRoutingRejectsWhatIsNotServed(t *testing.T) {
	server := newTestServer(loadGoldenModel(t, time.Now()), stubFetcher{}, time.Now())

	cases := []struct {
		name   string
		method string
		path   string
		status int
		detail string
	}{
		{"unknown path", http.MethodGet, "/api/nonsense", http.StatusNotFound, notFoundDetail},
		{"root", http.MethodGet, "/", http.StatusNotFound, notFoundDetail},
		{"wrong method on a served path", http.MethodPost, "/api/predict", http.StatusMethodNotAllowed, methodDetail},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := do(t, server, tc.method, tc.path)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.status)
			}
			var got errorResponse
			decodeInto(t, recorder, tc.status, &got)
			if got.Detail != tc.detail {
				t.Errorf("detail = %q, want %q", got.Detail, tc.detail)
			}
		})
	}
}
