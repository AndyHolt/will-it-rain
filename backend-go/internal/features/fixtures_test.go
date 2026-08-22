package features

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/AndyHolt/will-it-rain/backend-go/internal/forecast"
)

// The fixture readers below duplicate a few lines of internal/forecast's test
// helpers rather than sharing them. Exporting them would put a test-only type
// in the serving path, and the two packages read different sections of the
// same files: that one pins the parse, this one pins what is assembled from it.

// readTestdata reads one of the checked-in golden fixtures, which live at the
// module root so every package can share them.
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
// assembler.
//
// Taking the frame from expected.json rather than decoding testdata/forecast.fb
// is deliberate: decode is unexported, and its own test already pins the
// payload against exactly this column map. Composing the two pinned stages
// covers the chain without this package reaching into that one.
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

// goldenExpectation is testdata/expected.json: what the Python serving path
// made of the captured payload, stage by stage.
type goldenExpectation struct {
	Forecast      goldenForecast `json:"forecast"`
	NowUTC        string         `json:"now_utc"`
	AnchorUTC     string         `json:"anchor_utc"`
	FeatureCols   []string       `json:"feature_cols"`
	FeatureVector []*float64     `json:"feature_vector"`
}

func readExpected(t *testing.T) goldenExpectation {
	t.Helper()
	var expected goldenExpectation
	if err := json.Unmarshal(readTestdata(t, "expected.json"), &expected); err != nil {
		t.Fatalf("decoding expected.json: %v", err)
	}
	return expected
}

// goldenEdgeCases is testdata/edge_cases.json. Only the prediction cases are
// read here; the calibration cases belong to scoring.
type goldenEdgeCases struct {
	FeatureCols     []string `json:"feature_cols"`
	PredictionCases []struct {
		Name          string         `json:"name"`
		Description   string         `json:"description"`
		Forecast      goldenForecast `json:"forecast"`
		AnchorUTC     string         `json:"anchor_utc"`
		FeatureVector []*float64     `json:"feature_vector"`
	} `json:"prediction_cases"`
}

func readEdgeCases(t *testing.T) goldenEdgeCases {
	t.Helper()
	var cases goldenEdgeCases
	if err := json.Unmarshal(readTestdata(t, "edge_cases.json"), &cases); err != nil {
		t.Fatalf("decoding edge_cases.json: %v", err)
	}
	return cases
}

func readSpec(t *testing.T) *Spec {
	t.Helper()
	spec, err := ParseSpec(readTestdata(t, "serving.json"))
	if err != nil {
		t.Fatalf("ParseSpec on testdata/serving.json: %v", err)
	}
	return spec
}

func parseTime(t *testing.T, stamp string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("parsing timestamp %q: %v", stamp, err)
	}
	return parsed.UTC()
}

// assertVector compares an assembled vector against the golden one, naming the
// feature that disagreed rather than only its index.
//
// Exact, not approximate. Assembly moves values; it does not compute with
// them, so the two sides are the same float64 read from the same JSON and any
// difference at all is a lookup reading the wrong place.
func assertVector(t *testing.T, cols []string, got []float64, want []*float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("assembled %d features, want %d", len(got), len(want))
	}
	for i, want := range want {
		if !sameValue(got[i], want) {
			t.Errorf("%s = %v, want %v", cols[i], got[i], describe(want))
		}
	}
}

// sameValue compares an assembled value against the golden one, where a JSON
// null stands for the NaN Python held.
func sameValue(got float64, want *float64) bool {
	if want == nil {
		return math.IsNaN(got)
	}
	return got == *want
}

func describe(want *float64) string {
	if want == nil {
		return "NaN"
	}
	return strconv.FormatFloat(*want, 'g', -1, 64)
}
