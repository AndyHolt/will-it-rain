package predict

import (
	"bufio"
	"bytes"
	"fmt"

	"github.com/dmitryikh/leaves"
	"github.com/dmitryikh/leaves/transformation"
)

// allTrees scores with every tree in the ensemble. leaves takes a tree count so
// a caller can score a truncated ensemble; nothing here wants that, and
// predict_proba uses all of them.
const allTrees = 0

// The model-format version LightGBM 4.x writes, and the newest one leaves
// accepts. Matched with their newlines so only the header line can match.
var (
	writtenVersion = []byte("\nversion=v4\n")
	parsedVersion  = []byte("\nversion=v3\n")
)

// asParsableVersion relabels a v4 model header as v3.
//
// leaves package only supports LightGBM v3, but we use v4. No change to
// anything that leaves reads (only additional config in model.txt which leaves
// ignores).
//
// Only version 4 models are automatically used. A potential future v5 may not
// keep the same practical back compatibility, and would need to be
// investigated.
func asParsableVersion(modelText []byte) []byte {
	// Replace copies, so the caller's artefact bytes are left as fetched.
	return bytes.Replace(modelText, writtenVersion, parsedVersion, 1)
}

// Model is the trained LightGBM ensemble, ready to score one feature vector.
type Model struct {
	ensemble *leaves.Ensemble
}

// LoadModel parses model.txt and checks it against the serving contract it
// arrived with.
//
// nFeatures is len(feature_cols) from serving.json. The two files are uploaded
// together by model training pipeline so should always agree, but a version whose
// model.txt and serving.json describe different feature sets would score a
// vector whose columns are one position out and return a plausible wrong
// probability. Nothing downstream can detect that, and no golden fixture can
// either — the checked-in pair is consistent by construction, so only a live
// pair can drift.
//
// The transformation is loaded (rather than left raw), which is what makes the
// score a probability: for a "binary sigmoid:1" objective leaves applies the
// same logistic that predict_proba(...)[:, 1] does.
func LoadModel(modelText []byte, nFeatures int) (*Model, error) {
	text := bufio.NewReader(bytes.NewReader(asParsableVersion(modelText)))
	ensemble, err := leaves.LGEnsembleFromReader(text, true)
	if err != nil {
		return nil, fmt.Errorf("parsing model.txt: %w", err)
	}

	// A model trained on some other objective parses cleanly and comes back
	// with no transformation, so its score would be an unbounded log-odds
	// wearing the name raw_prob. Asserting the transform is also what keeps
	// PredictSingle usable below: the logistic yields exactly one output
	// group, the case it is defined for.
	if got := ensemble.Transformation().Type(); got != transformation.Logistic {
		return nil, fmt.Errorf(
			"model.txt is not a binary classifier: leaves applies the %s transformation, want %s",
			ensemble.Transformation().Name(), transformation.Logistic.Name(),
		)
	}
	if ensemble.NFeatures() != nFeatures {
		return nil, fmt.Errorf(
			"model.txt takes %d features but serving.json names %d feature_cols",
			ensemble.NFeatures(), nFeatures,
		)
	}

	return &Model{ensemble: ensemble}, nil
}

// Score returns the ensemble's probability of rain for one assembled feature
// vector: raw_prob, before calibration.
//
// NaN entries need no handling here. LightGBM stores a default direction with
// each split, so a missing feature follows the branch the model was fitted to
// send it down, and leaves reproduces that from the same decision_type field.
func (m *Model) Score(vector []float64) (float64, error) {
	// PredictSingle would answer 0.0 rather than failing on a short vector, and
	// silently ignores a long one. Either would reach the response as a
	// confident "no rain", so check length and error if not right.
	if len(vector) != m.ensemble.NFeatures() {
		return 0, fmt.Errorf(
			"feature vector has %d values, model.txt takes %d",
			len(vector), m.ensemble.NFeatures(),
		)
	}
	return m.ensemble.PredictSingle(vector, allTrees), nil
}
