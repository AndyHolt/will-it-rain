package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AndyHolt/will-it-rain/backend-go/internal/forecast"
	"github.com/AndyHolt/will-it-rain/backend-go/internal/registry"
	"github.com/AndyHolt/will-it-rain/backend-go/internal/server"
)

// The startup check's whole subject: a response that does not carry a column
// the model reads. Named at WARNING because it is the one gap no later fetch
// clears on its own — the forecast client's variable list having drifted from
// the spec a retrain published.
func TestReportColumnDriftWarnsAboutColumnsTheForecastLacks(t *testing.T) {
	logger, logs := captureLogs(t)

	// A response carrying no columns at all is the extreme of the same
	// condition, and needs no view of which seventeen it should have had.
	reportColumnDrift(goldenModel(t), &forecast.Forecast{}, logger)

	if !loggedAt(logs(), "WARNING", "the forecast does not carry every column") {
		t.Fatalf("no WARNING line about the drift: %v", logs())
	}
	columns, _ := logs()[0]["absent_columns"].([]any)
	if len(columns) == 0 {
		t.Errorf("the warning names no columns: %v", logs()[0])
	}
}

// A healthy response is silent. The captured payload's all-NaN showers column
// is present, so it is not drift — that gap is internal/server's to count per
// fetch, and reporting it here too would train the reader to ignore the line.
func TestReportColumnDriftIsQuietOnACompleteForecast(t *testing.T) {
	logger, logs := captureLogs(t)

	reportColumnDrift(goldenModel(t), goldenColumns(t), logger)

	if lines := logs(); len(lines) != 0 {
		t.Errorf("a complete forecast logged %v", lines)
	}
}

// Neither an unpromoted model nor a warm-up that could not reach Open-Meteo is
// drift, and both have already reported themselves in their own words.
func TestReportColumnDriftIsQuietWithNothingToCompare(t *testing.T) {
	for _, test := range []struct {
		name     string
		model    *server.Model
		forecast *forecast.Forecast
	}{
		{"no model promoted", nil, &forecast.Forecast{}},
		{"forecast warm-up failed", goldenModel(t), nil},
		{"neither", nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			logger, logs := captureLogs(t)
			reportColumnDrift(test.model, test.forecast, logger)
			if lines := logs(); len(lines) != 0 {
				t.Errorf("logged %v", lines)
			}
		})
	}
}

// goldenModel loads the checked-in fixture artefacts, which is the cheapest
// way to hold a Model carrying a real feature_cols.
func goldenModel(t *testing.T) *server.Model {
	t.Helper()
	model, err := server.NewModel(registry.Champion{
		ProductionModel: registry.ProductionModel{ResourceName: "models/fixture", VersionID: "3"},
		ModelText:       readTestdata(t, "model.txt"),
		ServingJSON:     readTestdata(t, "serving.json"),
	}, time.Now())
	if err != nil {
		t.Fatalf("NewModel on the golden fixtures: %v", err)
	}
	return model
}

// goldenColumns is a frame carrying the columns the captured response did.
// Only the keys matter here — the check is column presence, which is what lets
// it run before an anchor exists.
func goldenColumns(t *testing.T) *forecast.Forecast {
	t.Helper()
	var expected struct {
		Forecast struct {
			Columns map[string][]*float64 `json:"columns"`
		} `json:"forecast"`
	}
	if err := json.Unmarshal(readTestdata(t, "expected.json"), &expected); err != nil {
		t.Fatalf("decoding expected.json: %v", err)
	}

	columns := make(map[string][]float64, len(expected.Forecast.Columns))
	for name := range expected.Forecast.Columns {
		columns[name] = nil
	}
	return &forecast.Forecast{Columns: columns}
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return body
}
