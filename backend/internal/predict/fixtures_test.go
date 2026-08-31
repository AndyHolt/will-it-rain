package predict

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// The golden fixtures live at the module root so every package can read the
// section of them it is responsible for. This package reads the two ends of
// scoring: the feature vector that goes in, and the probability Python's
// predict_proba got out of the same model for it.

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return body
}

// goldenSpec is the slice of serving.json scoring needs to load the model: how
// many features the vector will carry. Parsing feature_cols here rather than
// importing internal/features keeps the test to one package under examination.
type goldenSpec struct {
	FeatureCols []string `json:"feature_cols"`
}

func readSpec(t *testing.T) goldenSpec {
	t.Helper()
	var spec goldenSpec
	if err := json.Unmarshal(readTestdata(t, "serving.json"), &spec); err != nil {
		t.Fatalf("decoding serving.json: %v", err)
	}
	return spec
}

// goldenExpectation is testdata/expected.json, read for the scoring stage: the
// vector Python assembled, the raw probability it scored, and what its
// calibrator made of that.
type goldenExpectation struct {
	FeatureVector  []*float64 `json:"feature_vector"`
	RawProb        float64    `json:"raw_prob"`
	CalibratedProb float64    `json:"calibrated_prob"`
}

func readExpected(t *testing.T) goldenExpectation {
	t.Helper()
	var expected goldenExpectation
	if err := json.Unmarshal(readTestdata(t, "expected.json"), &expected); err != nil {
		t.Fatalf("decoding expected.json: %v", err)
	}
	return expected
}

// goldenEdgeCases is testdata/edge_cases.json: the constructed cases covering
// what the captured payload cannot reach. Both halves of this package's work
// are in it — feature vectors carrying NaN, and the scores either side of the
// isotonic knots.
type goldenEdgeCases struct {
	PredictionCases []struct {
		Name          string     `json:"name"`
		Description   string     `json:"description"`
		FeatureVector []*float64 `json:"feature_vector"`
		RawProb       float64    `json:"raw_prob"`
	} `json:"prediction_cases"`

	CalibrationCases []struct {
		Name           string  `json:"name"`
		RawProb        float64 `json:"raw_prob"`
		CalibratedProb float64 `json:"calibrated_prob"`
	} `json:"calibration_cases"`
}

func readEdgeCases(t *testing.T) goldenEdgeCases {
	t.Helper()
	var cases goldenEdgeCases
	if err := json.Unmarshal(readTestdata(t, "edge_cases.json"), &cases); err != nil {
		t.Fatalf("decoding edge_cases.json: %v", err)
	}
	return cases
}

// vector rebuilds an assembled feature vector, where a JSON null stands for the
// NaN Python held — which is exactly the input the missing-value paths need.
func vector(golden []*float64) []float64 {
	values := make([]float64, len(golden))
	for i, value := range golden {
		values[i] = math.NaN()
		if value != nil {
			values[i] = *value
		}
	}
	return values
}

func loadGoldenCalibrator(t *testing.T) *Calibrator {
	t.Helper()
	calibrator, err := ParseCalibrator(readTestdata(t, "serving.json"))
	if err != nil {
		t.Fatalf("ParseCalibrator on testdata/serving.json: %v", err)
	}
	return calibrator
}

func loadGoldenModel(t *testing.T) *Model {
	t.Helper()
	model, err := LoadModel(readTestdata(t, "model.txt"), len(readSpec(t).FeatureCols))
	if err != nil {
		t.Fatalf("LoadModel on testdata/model.txt: %v", err)
	}
	return model
}
