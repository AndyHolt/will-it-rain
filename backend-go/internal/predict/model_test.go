package predict

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

// tolerance is the parity budget against Python. The two implementations walk
// the same trees in the same order and apply the same logistic, so what is
// being allowed for is float64 summation order, not a difference in method.
const tolerance = 1e-9

// TestScoreMatchesPython is the parity assertion this package exists to pass:
// leaves scoring the same model.txt on the same feature vector reaches the
// probability sklearn's predict_proba(...)[:, 1] did.
//
// It is also what settles the transformation question. If leaves returned a
// raw log-odds rather than a probability, this is where it would show.
func TestScoreMatchesPython(t *testing.T) {
	expected := readExpected(t)

	got, err := loadGoldenModel(t).Score(vector(expected.FeatureVector))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if math.Abs(got-expected.RawProb) > tolerance {
		t.Errorf("raw_prob = %.17g, want %.17g (Δ %g)",
			got, expected.RawProb, math.Abs(got-expected.RawProb))
	}
}

// TestScoreMatchesPythonWithMissingFeatures covers what the captured payload
// cannot: a NaN in a feature the model actually splits on. LightGBM sends it
// down each split's learned default direction, and the constructed fixture
// pins that leaves does the same rather than treating missing as zero — which
// would agree with Python on the control case and disagree on these.
func TestScoreMatchesPythonWithMissingFeatures(t *testing.T) {
	model := loadGoldenModel(t)

	for _, edge := range readEdgeCases(t).PredictionCases {
		t.Run(edge.Name, func(t *testing.T) {
			got, err := model.Score(vector(edge.FeatureVector))
			if err != nil {
				t.Fatalf("Score: %v", err)
			}
			if math.Abs(got-edge.RawProb) > tolerance {
				t.Errorf("%s\nraw_prob = %.17g, want %.17g (Δ %g)",
					edge.Description, got, edge.RawProb, math.Abs(got-edge.RawProb))
			}
		})
	}
}

// TestLoadModelRejectsFeatureCountDisagreement pins the check that a golden
// fixture cannot: the pair of files disagreeing about what the model takes.
func TestLoadModelRejectsFeatureCountDisagreement(t *testing.T) {
	_, err := LoadModel(readTestdata(t, "model.txt"), len(readSpec(t).FeatureCols)-1)
	if err == nil {
		t.Fatal("LoadModel accepted a model.txt taking more features than serving.json names")
	}
	if !strings.Contains(err.Error(), "feature_cols") {
		t.Errorf("error does not name the disagreement: %v", err)
	}
}

// TestLoadModelRejectsANonProbabilityObjective guards the other silent wrong
// answer: an objective leaves has no transformation for scores to an unbounded
// log-odds, which would calibrate and threshold as though it were a
// probability.
func TestLoadModelRejectsANonProbabilityObjective(t *testing.T) {
	regression := bytes.Replace(
		readTestdata(t, "model.txt"),
		[]byte("objective=binary sigmoid:1"), []byte("objective=regression"), 1,
	)

	_, err := LoadModel(regression, len(readSpec(t).FeatureCols))
	if err == nil {
		t.Fatal("LoadModel accepted a model that scores raw values rather than probabilities")
	}
}

// TestLoadModelAcceptsBothModelVersions covers the relabelling from either
// side: the v4 header every model trained here carries, and the v3 one leaves
// accepts unaided. Both must load — the shim rewrites a version, not a model.
func TestLoadModelAcceptsBothModelVersions(t *testing.T) {
	v4 := readTestdata(t, "model.txt")
	if !bytes.Contains(v4, []byte("\nversion=v4\n")) {
		t.Fatal("testdata/model.txt no longer declares version=v4; the shim is pinning nothing")
	}
	v3 := bytes.Replace(v4, []byte("\nversion=v4\n"), []byte("\nversion=v3\n"), 1)

	for version, modelText := range map[string][]byte{"v4": v4, "v3": v3} {
		t.Run(version, func(t *testing.T) {
			if _, err := LoadModel(modelText, len(readSpec(t).FeatureCols)); err != nil {
				t.Errorf("LoadModel on a %s model: %v", version, err)
			}
		})
	}
}

// TestLoadModelRejectsAnUnknownModelVersion is the other half of the shim.
// A format leaves has never seen must fail at load: relabelling it would feed
// an unread change to a parser that would not notice it.
func TestLoadModelRejectsAnUnknownModelVersion(t *testing.T) {
	future := bytes.Replace(
		readTestdata(t, "model.txt"),
		[]byte("\nversion=v4\n"), []byte("\nversion=v5\n"), 1,
	)

	if _, err := LoadModel(future, len(readSpec(t).FeatureCols)); err == nil {
		t.Fatal("LoadModel accepted a model format leaves has never been checked against")
	}
}

func TestLoadModelRejectsTextThatIsNotAModel(t *testing.T) {
	if _, err := LoadModel([]byte("not a lightgbm model"), 70); err == nil {
		t.Fatal("LoadModel accepted text that is not a LightGBM model")
	}
}

// TestScoreRejectsAWrongLengthVector covers leaves answering 0.0 rather than
// failing on a vector that is not the length the model takes. Reaching the
// response, that is a confident forecast of no rain.
func TestScoreRejectsAWrongLengthVector(t *testing.T) {
	model := loadGoldenModel(t)
	full := vector(readExpected(t).FeatureVector)

	for name, given := range map[string][]float64{
		"short": full[:len(full)-1],
		"long":  append(append([]float64{}, full...), 0),
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := model.Score(given); err == nil {
				t.Errorf("Score accepted a %d-value vector", len(given))
			}
		})
	}
}
