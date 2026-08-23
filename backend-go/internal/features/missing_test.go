package features

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

// The captured payload's gap is the one the live service actually meets:
// ukmo_uk_deterministic_2km__showers arrives as a column of NaN, so its four
// features have no value while nothing is absent or uncovered. Sorting that
// into the routine bucket rather than the warning one is the distinction this
// whole type exists to draw.
func TestMissingSortsTheGoldenForecastsGapIntoValues(t *testing.T) {
	expected := readExpected(t)
	spec := readSpec(t)
	frame := expected.Forecast.build(t)
	anchor := parseTime(t, expected.AnchorUTC)

	missing, err := spec.Missing(frame, anchor)
	if err != nil {
		t.Fatalf("Missing: %v", err)
	}

	if len(missing.Columns) != 0 || len(missing.Lags) != 0 {
		t.Errorf("Columns = %v, Lags = %v, want both empty: the capture carries every column",
			missing.Columns, missing.Lags)
	}
	// Counted against the vector rather than against a literal 4, so a
	// regenerated fixture moves both sides together — and so the two can never
	// report different numbers of missing features for the same row.
	vector, err := spec.Vector(frame, anchor)
	if err != nil {
		t.Fatalf("Vector: %v", err)
	}
	if want := countNaN(vector); missing.Values != want {
		t.Errorf("Values = %d, want %d, the NaNs in the assembled vector", missing.Values, want)
	}
	if !missing.Any() {
		t.Error("Any() = false with features missing")
	}
}

// The four conditions in one frame, so that a classification landing in the
// wrong bucket is visible as a swap rather than only as a wrong total. Each is
// NaN in the vector, and only this can say which is which.
func TestMissingTellsTheConditionsApart(t *testing.T) {
	spec := &Spec{
		FeatureCols: []string{
			"m__here",        // filled
			"m__absent",      // no such column
			"m__here__lag3h", // the forecast does not reach back that far
			"m__quiet",       // present, NaN at the anchor
			"m__sparse",      // fitted without, so not a gap in the forecast
			hourOfDayFeature, // derived, never missing
		},
		LagHours:      []int{1, 3},
		SparseColumns: []string{"m__sparse"},
	}
	start := parseTime(t, "2026-03-04T00:00:00Z")
	frame := frameOf(
		[]time.Time{start, start.Add(time.Hour)},
		map[string][]float64{
			"m__here":   {10, 11},
			"m__quiet":  {1, math.NaN()},
			"m__sparse": {5, 6},
		},
	)

	missing, err := spec.Missing(frame, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("Missing: %v", err)
	}

	if !slices.Equal(missing.Columns, []string{"m__absent"}) {
		t.Errorf("Columns = %v, want [m__absent]", missing.Columns)
	}
	if !slices.Equal(missing.Lags, []int{3}) {
		t.Errorf("Lags = %v, want [3]", missing.Lags)
	}
	// One: m__quiet. The sparse column and the seasonal feature are not gaps,
	// and the other two are accounted for above.
	if missing.Values != 1 {
		t.Errorf("Values = %d, want 1 (only m__quiet)", missing.Values)
	}
}

// A column feeds a base feature and one feature per lag, so an absent one
// would otherwise be named four times over in a log line whose whole job is to
// be readable.
func TestMissingNamesAnAbsentColumnOnce(t *testing.T) {
	spec := &Spec{
		FeatureCols: []string{"m__gone", "m__gone__lag1h", "m__gone__lag2h", "m__v"},
		LagHours:    []int{1, 2},
	}
	start := parseTime(t, "2026-03-04T00:00:00Z")

	missing, err := spec.Missing(hourly(start, 10, 11), start.Add(time.Hour))
	if err != nil {
		t.Fatalf("Missing: %v", err)
	}

	if !slices.Equal(missing.Columns, []string{"m__gone"}) {
		t.Errorf("Columns = %v, want [m__gone]", missing.Columns)
	}
	if missing.Values != 0 {
		t.Errorf("Values = %d, want 0: the absent column is not counted twice", missing.Values)
	}
}

// Nothing missing has to be reportable as nothing, or every fetch carries a
// warning and none of them mean anything.
func TestMissingIsEmptyWhenTheForecastSuppliesEverything(t *testing.T) {
	spec := &Spec{FeatureCols: []string{"m__v", "m__v__lag1h", monthFeature}, LagHours: []int{1}}
	start := parseTime(t, "2026-03-04T00:00:00Z")

	missing, err := spec.Missing(hourly(start, 10, 11), start.Add(time.Hour))
	if err != nil {
		t.Fatalf("Missing: %v", err)
	}
	if missing.Any() {
		t.Errorf("Any() = true on a complete frame: %+v", missing)
	}
}

// The same refusal Vector makes, for the same reason: with no anchor there is
// no row to account for, and reporting every feature missing would read as
// drift rather than as a forecast that does not reach the hour.
func TestMissingRejectsAnAnchorTheForecastDoesNotCover(t *testing.T) {
	spec := &Spec{FeatureCols: []string{"m__v"}}
	start := parseTime(t, "2026-03-04T00:00:00Z")

	if _, err := spec.Missing(hourly(start, 10, 11), start.Add(9*time.Hour)); err == nil {
		t.Fatal("Missing accepted an anchor outside the forecast")
	}
}

// AbsentColumns is the startup check, so it has to answer without an anchor —
// and answer the same as Missing does about the columns, or the two disagree
// about the one condition they both watch for.
func TestAbsentColumnsNamesWhatTheForecastDoesNotCarry(t *testing.T) {
	spec := &Spec{
		FeatureCols: []string{
			"m__here", "m__gone", "m__gone__lag1h", "m__also_gone", "m__sparse", hourOfDayFeature,
		},
		LagHours:      []int{1},
		SparseColumns: []string{"m__sparse"},
	}
	start := parseTime(t, "2026-03-04T00:00:00Z")
	frame := frameOf(
		[]time.Time{start},
		map[string][]float64{"m__here": {10}},
	)

	// Sorted, deduplicated, and without the sparse column — which is absent
	// from the forecast too, and is not a gap.
	if got := spec.AbsentColumns(frame); !slices.Equal(got, []string{"m__also_gone", "m__gone"}) {
		t.Errorf("AbsentColumns = %v, want [m__also_gone m__gone]", got)
	}

	missing, err := spec.Missing(frame, start)
	if err != nil {
		t.Fatalf("Missing: %v", err)
	}
	if !slices.Equal(missing.Columns, spec.AbsentColumns(frame)) {
		t.Errorf("Missing.Columns = %v, AbsentColumns = %v: the two must agree",
			missing.Columns, spec.AbsentColumns(frame))
	}
}

// The live capture is what says the startup check stays quiet on a healthy
// response: an all-NaN column is not an absent one.
func TestAbsentColumnsIsQuietOnTheGoldenForecast(t *testing.T) {
	expected := readExpected(t)

	if got := readSpec(t).AbsentColumns(expected.Forecast.build(t)); len(got) != 0 {
		t.Errorf("AbsentColumns = %v on the captured forecast, want none", got)
	}
}

func countNaN(values []float64) int {
	count := 0
	for _, value := range values {
		if math.IsNaN(value) {
			count++
		}
	}
	return count
}

// Guards the doc comment on Missing.Columns rather than any branch: the log
// line names columns, so a stray "__lag1h" in one would send a reader looking
// for a column that never existed.
func TestMissingColumnsAreBaseNames(t *testing.T) {
	spec := &Spec{FeatureCols: []string{"m__gone__lag1h"}, LagHours: []int{1}}
	start := parseTime(t, "2026-03-04T00:00:00Z")

	missing, err := spec.Missing(hourly(start, 10, 11), start.Add(time.Hour))
	if err != nil {
		t.Fatalf("Missing: %v", err)
	}
	for _, column := range missing.Columns {
		if strings.Contains(column, "__lag") {
			t.Errorf("Columns carries the lagged feature name %q, want the base column", column)
		}
	}
}
