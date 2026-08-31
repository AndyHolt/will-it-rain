package features

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/AndyHolt/will-it-rain/backend/internal/forecast"
)

// The two seasonal features build_features appends after the lags. They are
// derived (from the anchor timestamp) rather than read. So they are a special
// case that must be handled in code.
const (
	hourOfDayFeature = "hour_of_day"
	monthFeature     = "month"
)

// Spec is the feature-assembly half of serving.json: the columns the model
// expects, in the order it expects them, and what it was fitted without.
type Spec struct {
	// FeatureCols is the model's input order. Vector returns one value per
	// entry, in this order.
	FeatureCols []string `json:"feature_cols"`

	// LagHours are the offsets build_features made lagged copies at, and so
	// the only "__lagNh" suffixes that mean anything here.
	LagHours []int `json:"lag_hours"`

	// SparseColumns were dropped before fitting because their presence varied
	// over the training window, so the model never saw them.
	SparseColumns []string `json:"sparse_columns"`
}

// ParseSpec reads the feature-assembly fields out of serving.json.
//
// Unknown fields are ignored deliberately: threshold, prediction_window_hours
// and the isotonic knots share the file and belong to scoring, not to feature
// assembly. Each package declaring only the fields it consumes keeps that
// split visible in the types rather than in a comment.
func ParseSpec(servingJSON []byte) (*Spec, error) {
	var spec Spec
	if err := json.Unmarshal(servingJSON, &spec); err != nil {
		return nil, fmt.Errorf("parsing serving.json: %w", err)
	}
	if len(spec.FeatureCols) == 0 {
		return nil, errors.New("serving.json names no feature_cols")
	}
	for _, lag := range spec.LagHours {
		// A zero or negative lag would collide with the base column or read
		// forwards; either means the artefact is not what it claims to be, and
		// failing at load beats serving a quietly wrong vector.
		if lag <= 0 {
			return nil, fmt.Errorf("serving.json lag_hours contains %d, which is not a lag (must be positive)", lag)
		}
	}
	return &spec, nil
}

// Vector assembles the model's input row at anchor, in FeatureCols order.
//
// Missing values come back as NaN rather than as an error: LightGBM sends NaN
// down each split's learned default direction, so a feature the forecast does
// not carry is one the model was fitted to cope with.
func (s *Spec) Vector(f *forecast.Forecast, anchor time.Time) ([]float64, error) {
	anchor = anchor.UTC()
	rows, err := s.rowsByLag(f, anchor)
	if err != nil {
		return nil, err
	}

	vector := make([]float64, len(s.FeatureCols))
	for i, name := range s.FeatureCols {
		vector[i], _ = s.lookup(f, rows, anchor, name)
	}
	return vector, nil
}

// rowsByLag locates the row each lag reads from: the anchor's own row at 0,
// and the row lag hours before it otherwise. A lag reaching past the start of
// the forecast is left out of the map, and every feature at that lag is NaN.
//
// Lags resolve by *timestamp*, where build_features shifts by row. The two
// agree on any contiguous hourly frame, which is what the training frames and
// a live response both are. They part on a frame with a gap in it, where a row
// shift pulls the value from the far side and labels it "an hour ago". The
// timestamp is what the feature means, so that is what is looked up.
func (s *Spec) rowsByLag(f *forecast.Forecast, anchor time.Time) (map[int]int, error) {
	row, ok := rowAt(f, anchor)
	if !ok {
		return nil, fmt.Errorf(
			"forecast covers %s and does not include the anchor hour %s",
			describeRange(f), anchor.Format(time.RFC3339),
		)
	}

	rows := make(map[int]int, len(s.LagHours)+1)
	rows[0] = row
	for _, lag := range s.LagHours {
		if row, ok := rowAt(f, anchor.Add(-time.Duration(lag)*time.Hour)); ok {
			rows[lag] = row
		}
	}
	return rows, nil
}

// gap says why a feature could not be filled. Vector treats every gap the
// same — NaN, which is what the model was fitted to cope with — so this is
// only read by Missing, which has to tell Open-Meteo's routine sparseness
// apart from a forecast that no longer answers what the model asks for.
type gap uint8

const (
	// filled: a real value, and the only case carrying one.
	filled gap = iota

	// unfitted: dropped before fitting, so missing by definition rather than
	// by anything the forecast did or did not send.
	unfitted

	// absentColumn: the forecast carries no such column, at any hour.
	absentColumn

	// uncoveredHour: the column is there, but has no row at the hour this
	// feature reads — a lag reaching past the start of the forecast, or a
	// column that ends before Times does.
	uncoveredHour

	// noValue: column and hour are both there, and hold NaN.
	noValue
)

// lookup resolves one feature name, following the same four cases
// build_features produces, and says which of them it took.
func (s *Spec) lookup(f *forecast.Forecast, rows map[int]int, anchor time.Time, name string) (float64, gap) {
	switch name {
	case hourOfDayFeature:
		return float64(anchor.Hour()), filled
	case monthFeature:
		return float64(anchor.Month()), filled
	}

	column, lag := s.splitLag(name)
	// Dropped before the model ever saw them, so they are missing by
	// definition — the column arriving in the forecast anyway does not make it
	// a feature. build_features is handed the trimmed frame for the same
	// reason.
	if slices.Contains(s.SparseColumns, column) {
		return math.NaN(), unfitted
	}
	values, ok := f.Columns[column]
	if !ok {
		return math.NaN(), absentColumn
	}
	row, ok := rows[lag]
	// The bounds check restates Forecast's every-column-is-len(Times) rule
	// rather than trusting it: a feature that reads NaN is recoverable, and a
	// panic during startup is not.
	if !ok || row >= len(values) {
		return math.NaN(), uncoveredHour
	}
	if value := values[row]; !math.IsNaN(value) {
		return value, filled
	}
	return math.NaN(), noValue
}

// splitLag separates a lagged feature name into the column it lags and its lag
// in hours. A name carrying no declared lag suffix is a base column, at lag 0.
//
// Only the lags serving.json declares are recognised. A "__lag5h" suffix where
// lag_hours is [1,2,3] is not a lag this model was trained with, so it falls
// through as a column name in its own right — absent from the forecast, and so
// NaN, which is what asking a frame built without it would also have produced.
func (s *Spec) splitLag(name string) (string, int) {
	for _, lag := range s.LagHours {
		if column, ok := strings.CutSuffix(name, fmt.Sprintf("__lag%dh", lag)); ok {
			return column, lag
		}
	}
	return name, 0
}

// rowAt finds the row for an hour. Forecast.Times is ascending, so a miss
// hands back the insertion point, which PickAnchor uses to step backwards.
func rowAt(f *forecast.Forecast, at time.Time) (int, bool) {
	return slices.BinarySearchFunc(f.Times, at, func(t, target time.Time) int {
		return t.Compare(target)
	})
}

// describeRange renders the hours a forecast covers, for error messages that
// say how far off an anchor was rather than only that it missed.
func describeRange(f *forecast.Forecast) string {
	if len(f.Times) == 0 {
		return "no hours"
	}
	return fmt.Sprintf(
		"%s → %s",
		f.Times[0].Format(time.RFC3339), f.Times[len(f.Times)-1].Format(time.RFC3339),
	)
}
