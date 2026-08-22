package predict

import (
	"math"
	"testing"
)

// TestCalibrateMatchesPython is the parity assertion for the second half of
// the prediction: the knots interpolating to what
// calibrator.transform(raw_prob) returned for the same score.
func TestCalibrateMatchesPython(t *testing.T) {
	expected := readExpected(t)

	got := loadGoldenCalibrator(t).Calibrate(expected.RawProb)
	if math.Abs(got-expected.CalibratedProb) > tolerance {
		t.Errorf("calibrated_prob = %.17g, want %.17g (Δ %g)",
			got, expected.CalibratedProb, math.Abs(got-expected.CalibratedProb))
	}
}

// TestCalibrateMatchesPythonAtEdgeCases covers the shape of the step function
// that one in-range score cannot: both clamps, a score landing exactly on the
// first and last knots, three points across the steepest step — where an
// interpolation reading from the wrong end of the segment is furthest out —
// and the middle of the longest flat run.
func TestCalibrateMatchesPythonAtEdgeCases(t *testing.T) {
	calibrator := loadGoldenCalibrator(t)

	cases := readEdgeCases(t).CalibrationCases
	if len(cases) == 0 {
		t.Fatal("edge_cases.json carries no calibration cases")
	}
	for _, edge := range cases {
		t.Run(edge.Name, func(t *testing.T) {
			got := calibrator.Calibrate(edge.RawProb)
			if math.Abs(got-edge.CalibratedProb) > tolerance {
				t.Errorf("calibrated_prob = %.17g, want %.17g (Δ %g)",
					got, edge.CalibratedProb, math.Abs(got-edge.CalibratedProb))
			}
		})
	}
}

// TestCalibrateIsMonotone pins what makes the output a calibration rather than
// an arbitrary remapping: a higher score can never calibrate to a lower
// probability. A segment interpolated with its endpoints swapped would still
// pass the fixtures at the knots and fail here between them.
func TestCalibrateIsMonotone(t *testing.T) {
	calibrator := loadGoldenCalibrator(t)

	const steps = 1000
	previous := math.Inf(-1)
	for i := range steps + 1 {
		raw := float64(i) / steps
		got := calibrator.Calibrate(raw)
		if got < previous {
			t.Fatalf("Calibrate(%g) = %g, below the %g returned for a lower score", raw, got, previous)
		}
		if got < 0 || got > 1 {
			t.Fatalf("Calibrate(%g) = %g, which is not a probability", raw, got)
		}
		previous = got
	}
}

// TestCalibrateOfNaNIsNaN covers the one input the clamps cannot answer. It
// should not arise — Score's logistic always returns a number — but NaN
// silently taking the first branch of a comparison chain would report a
// confident 0.0 rather than an obviously broken value.
func TestCalibrateOfNaNIsNaN(t *testing.T) {
	if got := loadGoldenCalibrator(t).Calibrate(math.NaN()); !math.IsNaN(got) {
		t.Errorf("Calibrate(NaN) = %g, want NaN", got)
	}
}

// TestNewCalibratorRejectsUnusableKnots covers the artefacts that would
// otherwise calibrate arithmetically rather than fail: a repeated x dividing
// by zero, a shorter y indexing off its end, a descending x sending the binary
// search to the wrong segment.
//
// The checks are exercised here rather than through ParseCalibrator because
// two of them cannot be written as JSON at all — NaN has no literal — and
// splitting the table by whether its case is encodable would hide what the
// cases have in common.
func TestNewCalibratorRejectsUnusableKnots(t *testing.T) {
	for name, knots := range map[string]struct{ x, y []float64 }{
		"no knots":               {x: []float64{}, y: []float64{}},
		"one knot":               {x: []float64{0.5}, y: []float64{0.5}},
		"more x than y":          {x: []float64{0.1, 0.4, 0.9}, y: []float64{0.0, 1.0}},
		"more y than x":          {x: []float64{0.1, 0.9}, y: []float64{0.0, 0.5, 1.0}},
		"repeated x":             {x: []float64{0.1, 0.4, 0.4, 0.9}, y: []float64{0.0, 0.2, 0.8, 1.0}},
		"descending x":           {x: []float64{0.9, 0.4, 0.1}, y: []float64{1.0, 0.5, 0.0}},
		"x knot is not a number": {x: []float64{0.1, math.NaN()}, y: []float64{0.0, 1.0}},
		"y knot is not a number": {x: []float64{0.1, 0.9}, y: []float64{0.0, math.NaN()}},
		"x knot is infinite":     {x: []float64{0.1, math.Inf(1)}, y: []float64{0.0, 1.0}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newCalibrator(knots.x, knots.y); err == nil {
				t.Errorf("newCalibrator accepted %s", name)
			}
		})
	}
}

// TestParseCalibratorRejectsUnusableServingJSON is the same failure reaching
// the caller through the parser: a file that is not JSON, one carrying no
// knots at all, and one whose knots do not describe a function.
func TestParseCalibratorRejectsUnusableServingJSON(t *testing.T) {
	for name, servingJSON := range map[string]string{
		"not JSON":       "not json",
		"no isotonic":    `{"feature_cols": ["a"]}`,
		"knots disagree": `{"isotonic": {"x": [0.1, 0.4, 0.9], "y": [0.0, 1.0]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCalibrator([]byte(servingJSON)); err == nil {
				t.Errorf("ParseCalibrator accepted serving.json with %s", name)
			}
		})
	}
}

// TestCalibratorCopiesItsKnots pins that a Calibrator does not alias the
// slices it was built from; a caller mutating its own decode would otherwise
// change what the service calibrates with.
func TestCalibratorCopiesItsKnots(t *testing.T) {
	x := []float64{0.0, 1.0}
	y := []float64{0.0, 1.0}

	calibrator, err := newCalibrator(x, y)
	if err != nil {
		t.Fatalf("newCalibrator: %v", err)
	}
	y[1] = 0.0

	if got := calibrator.Calibrate(1.0); got != 1.0 {
		t.Errorf("Calibrate(1.0) = %g after the caller's knots changed, want 1", got)
	}
}
