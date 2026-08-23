package features

import (
	"maps"
	"slices"
	"time"

	"github.com/AndyHolt/will-it-rain/backend-go/internal/forecast"
)

// Missing accounts for the NaNs in the vector Vector assembles, sorted into
// the conditions that produced them.
//
// They are not the same news. A base column the forecast never carried means a
// partial response, or a variable list that has drifted from what a retrain
// put in serving.json — a bug, and one that does not fix itself. A column
// present but holding no value at the hour it reads is Open-Meteo's routine
// behaviour, already true of ukmo_uk_deterministic_2km__showers, and worth a
// count at most. Both arrive in the vector as NaN, so only the assembler is in
// a position to tell them apart.
//
// Sparse columns are absent from this accounting entirely: the model was
// fitted without them, so they are not a gap in what the forecast supplied.
type Missing struct {
	// Columns are the base columns feature_cols reads and the forecast does
	// not carry, sorted, one entry however many lags of it the model uses.
	Columns []string

	// Lags are the lag offsets whose hour the forecast has no row for, sorted.
	// Zero appears here only if the anchor's own row ran off the end of a
	// column shorter than the frame.
	Lags []int

	// Values counts the features whose column and hour are both present and
	// hold NaN.
	Values int
}

// Any reports whether anything was missing at all.
func (m Missing) Any() bool {
	return len(m.Columns) > 0 || len(m.Lags) > 0 || m.Values > 0
}

// Missing accounts for what Vector could not fill at anchor.
//
// It walks feature_cols through the same lookup Vector does, so the two cannot
// disagree about which features are missing or why.
//
// The error is Vector's: an anchor the forecast does not cover is a failure to
// serve rather than a sparse row, and there is no prediction to report on.
func (s *Spec) Missing(f *forecast.Forecast, anchor time.Time) (Missing, error) {
	anchor = anchor.UTC()
	rows, err := s.rowsByLag(f, anchor)
	if err != nil {
		return Missing{}, err
	}

	var missing Missing
	columns := make(map[string]struct{})
	for _, name := range s.FeatureCols {
		column, lag := s.splitLag(name)
		switch _, why := s.lookup(f, rows, anchor, name); why {
		case absentColumn:
			columns[column] = struct{}{}
		case uncoveredHour:
			if !slices.Contains(missing.Lags, lag) {
				missing.Lags = append(missing.Lags, lag)
			}
		case noValue:
			missing.Values++
		}
	}

	missing.Columns = slices.Sorted(maps.Keys(columns))
	slices.Sort(missing.Lags)
	return missing, nil
}

// AbsentColumns names the base columns feature_cols reads and f does not carry
// at all.
//
// The half of Missing that needs no anchor, which is what makes it answerable
// at startup, before a request has picked one. What it watches for is the
// forecast client's hardcoded variable list drifting from the spec a retrain
// published — the one condition here that no later fetch will clear.
func (s *Spec) AbsentColumns(f *forecast.Forecast) []string {
	absent := make(map[string]struct{})
	for _, name := range s.FeatureCols {
		if name == hourOfDayFeature || name == monthFeature {
			continue
		}
		column, _ := s.splitLag(name)
		if slices.Contains(s.SparseColumns, column) {
			continue
		}
		if _, ok := f.Columns[column]; !ok {
			absent[column] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(absent))
}
