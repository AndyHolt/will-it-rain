package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/AndyHolt/will-it-rain/backend-go/internal/forecast"
)

// recordLogs returns a logger and a reader of what it captured, so the report
// can be asserted on as the structured lines Cloud Logging will index rather
// than as a formatted string.
func recordLogs(t *testing.T) (*slog.Logger, func() []map[string]any) {
	t.Helper()
	var recorded strings.Builder
	logger := slog.New(slog.NewJSONHandler(&recorded, nil))

	return logger, func() []map[string]any {
		var lines []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(recorded.String()), "\n") {
			if line == "" {
				continue
			}
			var fields map[string]any
			if err := json.Unmarshal([]byte(line), &fields); err != nil {
				t.Fatalf("decoding log line %q: %v", line, err)
			}
			lines = append(lines, fields)
		}
		return lines
	}
}

// linesAbout returns the recorded lines whose message starts the given way.
func linesAbout(lines []map[string]any, message string) []map[string]any {
	var matched []map[string]any
	for _, line := range lines {
		if got, _ := line["msg"].(string); strings.HasPrefix(got, message) {
			matched = append(matched, line)
		}
	}
	return matched
}

// newRecordingServer is newTestServer with the logger readable.
func newRecordingServer(t *testing.T, fetcher Fetcher, now time.Time) (*Server, func() []map[string]any) {
	t.Helper()
	logger, logs := recordLogs(t)
	server := New(loadGoldenModel(t, time.Now()), fetcher, logger)
	server.now = func() time.Time { return now }
	return server, logs
}

const (
	valuesMessage = "features with no value"
	absentMessage = "the forecast does not supply"
)

// The captured forecast is the live case: four of seventy features have no
// value because ukmo_uk_deterministic_2km__showers comes back empty. That is
// Open-Meteo's routine behaviour, so it is counted at INFO and nothing is
// warned about.
func TestPredictCountsTheFeaturesWithNoValue(t *testing.T) {
	expected := readExpected(t)
	server, logs := newRecordingServer(t,
		stubFetcher{forecast: expected.Forecast.build(t)}, parseTime(t, expected.NowUTC))

	get(t, server, "/api/predict")

	reported := linesAbout(logs(), valuesMessage)
	if len(reported) != 1 {
		t.Fatalf("got %d lines counting missing values, want 1: %v", len(reported), logs())
	}
	if got := reported[0]["features_without_value"]; got != float64(4) {
		t.Errorf("features_without_value = %v, want 4", got)
	}
	if got := reported[0]["features_total"]; got != float64(70) {
		t.Errorf("features_total = %v, want 70", got)
	}
	if got := reported[0]["level"]; got != "INFO" {
		t.Errorf("level = %v, want INFO: a value Open-Meteo did not send is routine", got)
	}
	if warned := linesAbout(logs(), absentMessage); len(warned) != 0 {
		t.Errorf("warned about a complete forecast: %v", warned)
	}
}

// A column the response does not carry is a partial answer or a client that
// has drifted from the spec — a bug, so it is named and warned about rather
// than counted.
func TestPredictWarnsWhenTheForecastLacksAColumn(t *testing.T) {
	expected := readExpected(t)
	frame := expected.Forecast.build(t)
	const dropped = "ecmwf_ifs__cloud_cover"
	delete(frame.Columns, dropped)

	server, logs := newRecordingServer(t,
		stubFetcher{forecast: frame}, parseTime(t, expected.NowUTC))

	// Still 200: four features are already missing routinely, so on a service
	// that scales to zero, refusing would turn drift into an outage at the
	// next cold start.
	if recorder := get(t, server, "/api/predict"); recorder.Code != 200 {
		t.Fatalf("status = %d, want 200: a missing column is served over, not refused", recorder.Code)
	}

	warned := linesAbout(logs(), absentMessage)
	if len(warned) != 1 {
		t.Fatalf("got %d lines about the absent column, want 1: %v", len(warned), logs())
	}
	if got := warned[0]["level"]; got != "WARN" {
		t.Errorf("level = %v, want WARN", got)
	}
	columns, _ := warned[0]["absent_columns"].([]any)
	if len(columns) != 1 || columns[0] != dropped {
		t.Errorf("absent_columns = %v, want [%s]", warned[0]["absent_columns"], dropped)
	}
	if got := warned[0]["model_version"]; got != fixtureVersionID {
		t.Errorf("model_version = %v, want %q", got, fixtureVersionID)
	}
}

// The report is per fetch, not per request: the forecast is cached for ten
// minutes, so its completeness changes at that cadence and repeating the
// finding for every caller in the window would say nothing new each time.
func TestPredictReportsOncePerFetch(t *testing.T) {
	expected := readExpected(t)
	fetcher := &mutableFetcher{forecast: expected.Forecast.build(t)}
	server, logs := newRecordingServer(t, fetcher, parseTime(t, expected.NowUTC))

	for range 3 {
		get(t, server, "/api/predict")
	}
	if reported := linesAbout(logs(), valuesMessage); len(reported) != 1 {
		t.Fatalf("three requests over one forecast reported %d times, want 1", len(reported))
	}

	// A refresh is a different *Forecast, which is what the cache hands out
	// once its entry ages out — and what the report keys on.
	fetcher.forecast = expected.Forecast.build(t)
	get(t, server, "/api/predict")

	if reported := linesAbout(logs(), valuesMessage); len(reported) != 2 {
		t.Errorf("a refreshed forecast reported %d times in total, want 2", len(reported))
	}
}

// Nothing missing has to be silent, or the report is noise that a real gap
// hides in.
func TestPredictIsQuietWhenNothingIsMissing(t *testing.T) {
	expected := readExpected(t)
	frame := expected.Forecast.build(t)
	fill(frame, "ukmo_uk_deterministic_2km__showers")

	server, logs := newRecordingServer(t,
		stubFetcher{forecast: frame}, parseTime(t, expected.NowUTC))

	get(t, server, "/api/predict")

	if lines := logs(); len(lines) != 0 {
		t.Errorf("a complete forecast logged %v", lines)
	}
}

// mutableFetcher hands back whatever forecast it currently holds, standing in
// for the cache ageing out and refetching.
type mutableFetcher struct {
	forecast *forecast.Forecast
}

func (m *mutableFetcher) Fetch(context.Context) (*forecast.Forecast, error) {
	return m.forecast, nil
}

// fill replaces a column with zeros, so a frame can be made complete without
// rebuilding it.
func fill(f *forecast.Forecast, column string) {
	f.Columns[column] = make([]float64, len(f.Times))
}
