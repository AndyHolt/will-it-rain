package server

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AndyHolt/will-it-rain/backend/internal/forecast"
	"github.com/AndyHolt/will-it-rain/backend/internal/registry"
)

// This package's stake in the golden fixtures is the whole chain: the same
// artefacts and the same captured forecast that internal/features and
// internal/predict each pin one stage of, run end to end through the handler
// and compared against the response Python's serving path produced.

const (
	// The registry metadata a Champion carries, which never comes from the
	// fixtures — those are the artefacts, not the version that published them.
	fixtureVersionID    = "3"
	fixtureResourceName = "projects/will-it-rain/locations/europe-west2/models/1234567890"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return body
}

// goldenForecast is a forecast frame as the Python generator serialises it.
// Values are *float64 so a JSON null — the NaN Python held — stays distinct
// from a reported zero, which for rain is the common case.
type goldenForecast struct {
	TimesUTC []string              `json:"times_utc"`
	Columns  map[string][]*float64 `json:"columns"`
}

// build turns the serialised frame back into what internal/forecast hands the
// server. Taking it from expected.json rather than decoding testdata/forecast.fb
// keeps this package to one thing under examination: that decode has its own
// test pinning it against exactly this column map.
func (g goldenForecast) build(t *testing.T) *forecast.Forecast {
	t.Helper()
	times := make([]time.Time, len(g.TimesUTC))
	for i, stamp := range g.TimesUTC {
		times[i] = parseTime(t, stamp)
	}
	columns := make(map[string][]float64, len(g.Columns))
	for name, values := range g.Columns {
		column := make([]float64, len(values))
		for i, value := range values {
			column[i] = math.NaN()
			if value != nil {
				column[i] = *value
			}
		}
		columns[name] = column
	}
	return &forecast.Forecast{Times: times, Columns: columns}
}

// goldenExpectation is testdata/expected.json read for the response: the
// forecast that went in, the clock it was read at, and every field the Python
// backend answered with.
type goldenExpectation struct {
	Forecast       goldenForecast `json:"forecast"`
	NowUTC         string         `json:"now_utc"`
	AnchorUTC      string         `json:"anchor_utc"`
	WindowEndUTC   string         `json:"window_end_utc"`
	RawProb        float64        `json:"raw_prob"`
	CalibratedProb float64        `json:"calibrated_prob"`
	Threshold      float64        `json:"threshold"`
	WillRain       bool           `json:"will_rain"`
}

func readExpected(t *testing.T) goldenExpectation {
	t.Helper()
	var expected goldenExpectation
	if err := json.Unmarshal(readTestdata(t, "expected.json"), &expected); err != nil {
		t.Fatalf("decoding expected.json: %v", err)
	}
	return expected
}

func parseTime(t *testing.T, stamp string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("parsing timestamp %q: %v", stamp, err)
	}
	return parsed.UTC()
}

// goldenChampion is the fixture artefacts as they arrive from the registry.
func goldenChampion(t *testing.T) registry.Champion {
	t.Helper()
	return registry.Champion{
		ProductionModel: registry.ProductionModel{
			ResourceName: fixtureResourceName,
			VersionID:    fixtureVersionID,
		},
		ModelText:   readTestdata(t, "model.txt"),
		ServingJSON: readTestdata(t, "serving.json"),
	}
}

func loadGoldenModel(t *testing.T, loadedAt time.Time) *Model {
	t.Helper()
	model, err := NewModel(goldenChampion(t), loadedAt)
	if err != nil {
		t.Fatalf("NewModel on the golden fixtures: %v", err)
	}
	return model
}

// servingJSONWith is the fixture's serving.json under one edit: a key set to a
// new value, or removed where the value is nil. Editing the real artefact
// rather than writing a minimal one by hand keeps these cases a hair's breadth
// from what the pipeline publishes, so a rejection is attributable to the edit.
func servingJSONWith(t *testing.T, key string, value any) []byte {
	t.Helper()
	var serving map[string]any
	if err := json.Unmarshal(readTestdata(t, "serving.json"), &serving); err != nil {
		t.Fatalf("decoding serving.json: %v", err)
	}
	if value == nil {
		delete(serving, key)
	} else {
		serving[key] = value
	}
	edited, err := json.Marshal(serving)
	if err != nil {
		t.Fatalf("re-encoding serving.json: %v", err)
	}
	return edited
}
