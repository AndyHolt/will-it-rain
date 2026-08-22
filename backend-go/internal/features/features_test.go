package features

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AndyHolt/will-it-rain/backend-go/internal/forecast"
)

// This is the parity assertion the package exists to satisfy: the captured
// forecast has to assemble into the same seventy values here as it does
// through will_it_rain_shared/features.py, in the same order, or the model is
// scored on a row it was not fitted for.
func TestVectorMatchesTheGoldenFeatureVector(t *testing.T) {
	expected := readExpected(t)
	spec := readSpec(t)

	// serving.json and expected.json come out of one generator run, so a
	// disagreement here means the fixtures were regenerated apart and every
	// comparison below is against the wrong model.
	if !slices.Equal(spec.FeatureCols, expected.FeatureCols) {
		t.Fatalf("serving.json and expected.json disagree on feature_cols")
	}

	got, err := spec.Vector(expected.Forecast.build(t), parseTime(t, expected.AnchorUTC))
	if err != nil {
		t.Fatalf("Vector: %v", err)
	}
	assertVector(t, spec.FeatureCols, got, expected.FeatureVector)
}

// The captured payload's missing values are Open-Meteo's behaviour on the day,
// not a property of the contract — these cases place NaN deliberately, once in
// a base feature and once in a lagged one, so a bug in either lookup is
// distinguishable. Their forecasts also sit in a different month and hour from
// the capture, which is what makes a transposed hour_of_day/month visible.
func TestVectorMatchesTheConstructedEdgeCases(t *testing.T) {
	cases := readEdgeCases(t)
	spec := readSpec(t)

	if !slices.Equal(spec.FeatureCols, cases.FeatureCols) {
		t.Fatalf("serving.json and edge_cases.json disagree on feature_cols")
	}

	for _, edge := range cases.PredictionCases {
		t.Run(edge.Name, func(t *testing.T) {
			t.Log(edge.Description)
			got, err := spec.Vector(edge.Forecast.build(t), parseTime(t, edge.AnchorUTC))
			if err != nil {
				t.Fatalf("Vector: %v", err)
			}
			assertVector(t, spec.FeatureCols, got, edge.FeatureVector)
		})
	}
}

// An anchor off by an hour changes every lagged feature at once, which the
// aggregate above cannot show. Here each hour carries a value that says which
// hour it is.
func TestVectorReadsEachLagFromItsOwnHour(t *testing.T) {
	spec := &Spec{
		FeatureCols: []string{
			"m__v", "m__v__lag1h", "m__v__lag2h", "m__v__lag3h", hourOfDayFeature, monthFeature,
		},
		LagHours: []int{1, 2, 3},
	}
	frame := hourly(parseTime(t, "2026-03-04T00:00:00Z"), 10, 11, 12, 13, 14, 15)

	got, err := spec.Vector(frame, parseTime(t, "2026-03-04T04:00:00Z"))
	if err != nil {
		t.Fatalf("Vector: %v", err)
	}
	// 04:00 reads 14; its lags walk back one hour at a time; March is month 3.
	assertVector(t, spec.FeatureCols, got, floats(14, 13, 12, 11, 4, 3))
}

// A lag reaching past the start of the frame is missing, not clamped to the
// first row — which is what past_hours=24 buys headroom against.
func TestVectorIsNaNWhereTheLagPredatesTheForecast(t *testing.T) {
	spec := &Spec{
		FeatureCols: []string{"m__v", "m__v__lag1h", "m__v__lag2h", "m__v__lag3h"},
		LagHours:    []int{1, 2, 3},
	}
	frame := hourly(parseTime(t, "2026-03-04T00:00:00Z"), 10, 11, 12)

	got, err := spec.Vector(frame, parseTime(t, "2026-03-04T01:00:00Z"))
	if err != nil {
		t.Fatalf("Vector: %v", err)
	}
	assertVector(t, spec.FeatureCols, got, []*float64{value(11), value(10), nil, nil})
}

// The deliberate divergence from build_features' row shift: on a frame with an
// hour missing, a lag crossing the gap is missing too, rather than quietly
// reading the far side of it and calling that an hour ago.
func TestVectorLooksLagsUpByTimestampNotByRow(t *testing.T) {
	spec := &Spec{
		FeatureCols: []string{"m__v", "m__v__lag1h", "m__v__lag2h"},
		LagHours:    []int{1, 2},
	}
	start := parseTime(t, "2026-03-04T00:00:00Z")
	// 02:00 is absent, so the rows read 00:00, 01:00, 03:00.
	frame := frameOf(
		[]time.Time{start, start.Add(time.Hour), start.Add(3 * time.Hour)},
		map[string][]float64{"m__v": {10, 11, 13}},
	)

	got, err := spec.Vector(frame, start.Add(3*time.Hour))
	if err != nil {
		t.Fatalf("Vector: %v", err)
	}
	// lag1h wants the absent 02:00; lag2h wants 01:00, which is present. A row
	// shift would have read 11 and 10.
	assertVector(t, spec.FeatureCols, got, []*float64{value(13), nil, value(11)})
}

// A sparse column is one the model never saw, so it is missing whether or not
// Open-Meteo sends it — matching predict_from_model dropping it before the
// frame reaches the builder.
func TestVectorIsNaNForColumnsTheModelWasFittedWithout(t *testing.T) {
	spec := &Spec{
		FeatureCols:   []string{"m__v", "m__sparse", "m__sparse__lag1h"},
		LagHours:      []int{1},
		SparseColumns: []string{"m__sparse"},
	}
	start := parseTime(t, "2026-03-04T00:00:00Z")
	frame := frameOf(
		[]time.Time{start, start.Add(time.Hour)},
		map[string][]float64{"m__v": {10, 11}, "m__sparse": {1, 2}},
	)

	got, err := spec.Vector(frame, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("Vector: %v", err)
	}
	assertVector(t, spec.FeatureCols, got, []*float64{value(11), nil, nil})
}

// Anything the forecast does not carry is a missing value, which LightGBM
// handles natively — including a lag suffix this model was not trained with,
// which is a column name of its own rather than a lag to resolve.
func TestVectorIsNaNForFeaturesTheForecastCannotSupply(t *testing.T) {
	spec := &Spec{
		FeatureCols: []string{"m__v", "m__absent", "m__v__lag5h"},
		LagHours:    []int{1},
	}
	start := parseTime(t, "2026-03-04T00:00:00Z")
	frame := hourly(start, 10, 11)

	got, err := spec.Vector(frame, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("Vector: %v", err)
	}
	assertVector(t, spec.FeatureCols, got, []*float64{value(11), nil, nil})
}

// The anchor is the one hour that has to be there: without it every feature is
// missing at once, which is a failure to serve rather than a sparse row. The
// Python path raises KeyError in the same place.
func TestVectorRejectsAnAnchorTheForecastDoesNotCover(t *testing.T) {
	spec := &Spec{FeatureCols: []string{"m__v"}}
	start := parseTime(t, "2026-03-04T00:00:00Z")
	frame := hourly(start, 10, 11)

	_, err := spec.Vector(frame, start.Add(9*time.Hour))
	if err == nil {
		t.Fatal("Vector accepted an anchor outside the forecast")
	}
	// The range is in the message because "anchor missing" and "anchor is a
	// day out" are diagnosed differently.
	if !strings.Contains(err.Error(), "2026-03-04T01:00:00Z") {
		t.Errorf("error %q does not report the hours covered", err)
	}
}

func TestParseSpecReadsTheAssemblyFieldsAndIgnoresTheRest(t *testing.T) {
	spec := readSpec(t)

	if len(spec.FeatureCols) == 0 {
		t.Fatal("ParseSpec read no feature_cols")
	}
	if !slices.Equal(spec.LagHours, []int{1, 2, 3}) {
		t.Errorf("LagHours = %v, want [1 2 3]", spec.LagHours)
	}
	if !slices.Contains(spec.SparseColumns, "ecmwf_ifs__showers") {
		t.Errorf("SparseColumns = %v, want it to carry the sparse showers column", spec.SparseColumns)
	}
	// threshold, prediction_window_hours and isotonic are in the same file and
	// must not make it a parse failure here.
	if !slices.Contains(spec.FeatureCols, hourOfDayFeature) {
		t.Errorf("feature_cols does not carry %s", hourOfDayFeature)
	}
}

func TestParseSpecRejectsAnArtefactItCannotAssembleFrom(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{"not json", `{`},
		{"no feature_cols", `{"lag_hours":[1],"sparse_columns":[]}`},
		{"empty feature_cols", `{"feature_cols":[],"lag_hours":[1]}`},
		{"zero lag", `{"feature_cols":["m__v"],"lag_hours":[0]}`},
		{"negative lag", `{"feature_cols":["m__v"],"lag_hours":[1,-2]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseSpec([]byte(test.body)); err == nil {
				t.Errorf("ParseSpec(%s) succeeded", test.body)
			}
		})
	}
}

// hourly builds a single-column frame on contiguous hours from start.
func hourly(start time.Time, values ...float64) *forecast.Forecast {
	times := make([]time.Time, len(values))
	for i := range values {
		times[i] = start.Add(time.Duration(i) * time.Hour)
	}
	return frameOf(times, map[string][]float64{"m__v": values})
}

func frameOf(times []time.Time, columns map[string][]float64) *forecast.Forecast {
	return &forecast.Forecast{Times: times, Columns: columns}
}

// floats renders present values in the shape assertVector compares against.
func floats(values ...float64) []*float64 {
	want := make([]*float64, len(values))
	for i := range values {
		want[i] = value(values[i])
	}
	return want
}

func value(v float64) *float64 { return &v }
