package predict

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
)

// Calibrator maps the ensemble's raw probability onto the calibrated one.
//
// The isotonic regression the pipeline fits is a step function, and
// save_serving_artefacts reduces it to its knots: the (x, y) pairs where it
// changes. Evaluating it is then linear interpolation between neighbouring
// knots, clamped at both ends — which is what sklearn's transform does for an
// IsotonicRegression fitted with out_of_bounds="clip", and what the Python
// side pins with np.interp.
type Calibrator struct {
	// x is strictly increasing, and x and y are the same length. Both are
	// established at construction so Calibrate can index without checking.
	x, y []float64
}

// servingCalibration is the calibration half of serving.json.
//
// Only the isotonic knots are read: feature_cols, lag_hours and
// sparse_columns belong to internal/features.
type servingCalibration struct {
	Isotonic struct {
		X []float64 `json:"x"`
		Y []float64 `json:"y"`
	} `json:"isotonic"`
}

// ParseCalibrator reads the isotonic knots out of serving.json.
func ParseCalibrator(servingJSON []byte) (*Calibrator, error) {
	var parsed servingCalibration
	if err := json.Unmarshal(servingJSON, &parsed); err != nil {
		return nil, fmt.Errorf("parsing serving.json: %w", err)
	}
	return newCalibrator(parsed.Isotonic.X, parsed.Isotonic.Y)
}

// newCalibrator checks the knots describe a function that can be interpolated
// before anything asks it for a probability.
//
// The checks are not defensive padding: a calibrator is the last thing between
// a score and the number a user reads, and every one of these failures would
// otherwise surface as an arithmetic result rather than as an error — a
// division by zero from a repeated x, an index off the end of a shorter y.
// Failing here fails at startup, where the artefact that caused it is still in
// hand.
func newCalibrator(x, y []float64) (*Calibrator, error) {
	if len(x) != len(y) {
		return nil, fmt.Errorf("serving.json isotonic has %d x knots and %d y knots", len(x), len(y))
	}
	// Two knots are the fewest that define a segment. One would still clamp to
	// a constant, but a calibrator that answers the same probability to every
	// score is not one the pipeline can have fitted.
	if len(x) < 2 {
		return nil, errors.New("serving.json isotonic needs at least 2 knots")
	}
	for i, value := range x {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("serving.json isotonic x knot %d is %g, not a finite value", i, value)
		}
		if i > 0 && value <= x[i-1] {
			return nil, fmt.Errorf(
				"serving.json isotonic x knots are not strictly increasing: knot %d is %g, after %g",
				i, value, x[i-1],
			)
		}
	}
	for i, value := range y {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("serving.json isotonic y knot %d is %g, not a finite value", i, value)
		}
	}
	return &Calibrator{x: slices.Clone(x), y: slices.Clone(y)}, nil
}

// Calibrate returns the calibrated probability for a raw ensemble score.
//
// A score outside the knots clamps to the nearest end rather than
// extrapolating: the calibrator was fitted on the scores the training set
// produced and says nothing about what lies beyond them. The knots span most
// but not all of [0, 1], so this is a live path, not a guard — the fixture's
// own first knot sits at 0.0027.
func (c *Calibrator) Calibrate(raw float64) float64 {
	last := len(c.x) - 1
	switch {
	// NaN compares false against everything below, so it would fall through to
	// a search that has no answer for it. np.interp hands NaN back, and so
	// does this: a score that is not a number cannot calibrate into one.
	case math.IsNaN(raw):
		return raw
	case raw <= c.x[0]:
		return c.y[0]
	case raw >= c.x[last]:
		return c.y[last]
	}

	// The segment raw falls in: lo is the last knot at or below it. raw is
	// strictly inside the knots here, so lo is in [0, last-1] either way and
	// hi stays in range.
	lo, onKnot := slices.BinarySearch(c.x, raw)
	if !onKnot {
		// The search returned the first knot above raw; the segment starts at
		// the one before it.
		lo--
	}
	hi := lo + 1

	// Interpolating from the lower knot, as np.interp does. Taking the
	// segment at or below raw (rather than above it) is what makes a score
	// landing exactly on a knot return that knot's y exactly, with no
	// rounding through the slope.
	slope := (c.y[hi] - c.y[lo]) / (c.x[hi] - c.x[lo])
	return slope*(raw-c.x[lo]) + c.y[lo]
}
