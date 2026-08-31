package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/AndyHolt/will-it-rain/backend/internal/features"
	"github.com/AndyHolt/will-it-rain/backend/internal/forecast"
	"github.com/AndyHolt/will-it-rain/backend/internal/predict"
	"github.com/AndyHolt/will-it-rain/backend/internal/registry"
)

// Model is the @production version this instance serves: a champion's two
// artefacts unpacked into everything one prediction needs, plus the registry
// metadata /api/health reports.
type Model struct {
	// VersionID and ResourceName are what the registry called this version,
	// and LoadedAt is when this process read it. The three of them are
	// /api/health's whole answer.
	VersionID    string
	ResourceName string
	LoadedAt     time.Time

	spec       *features.Spec
	booster    *predict.Model
	calibrator *predict.Calibrator

	// threshold and window are serving.json's decision half: what calibrated
	// probability counts as rain, and how far ahead the answer reaches.
	threshold float64
	window    time.Duration
}

// NewModel unpacks a champion's artefacts into a servable model.
//
// Every parse and cross-check the serving path needs happens here, at startup,
// with the artefacts still in hand: a model.txt whose feature count
// contradicts serving.json, knots that do not describe a function, a threshold
// that is not a probability. None of those are detectable downstream — a wrong
// probability reads exactly like a right one — so an instance that comes up at
// all is one whose artefacts agree.
func NewModel(champion registry.Champion, loadedAt time.Time) (*Model, error) {
	spec, err := features.ParseSpec(champion.ServingJSON)
	if err != nil {
		return nil, err
	}

	booster, err := predict.LoadModel(champion.ModelText, len(spec.FeatureCols))
	if err != nil {
		return nil, err
	}

	calibrator, err := predict.ParseCalibrator(champion.ServingJSON)
	if err != nil {
		return nil, err
	}

	decision, err := parseDecision(champion.ServingJSON)
	if err != nil {
		return nil, err
	}

	return &Model{
		VersionID:    champion.VersionID,
		ResourceName: champion.ResourceName,
		LoadedAt:     loadedAt.UTC(),
		spec:         spec,
		booster:      booster,
		calibrator:   calibrator,
		threshold:    decision.threshold,
		window:       decision.window,
	}, nil
}

// Predict answers for the hour now falls in: pick the anchor, assemble its
// feature vector, score it, calibrate the score.
func (m *Model) Predict(f *forecast.Forecast, now time.Time) (PredictResponse, error) {
	anchor, err := features.PickAnchor(f, now)
	if err != nil {
		return PredictResponse{}, fmt.Errorf("picking anchor: %w", err)
	}

	vector, err := m.spec.Vector(f, anchor)
	if err != nil {
		return PredictResponse{}, fmt.Errorf("assembling features at %s: %w", anchor.Format(time.RFC3339), err)
	}

	raw, err := m.booster.Score(vector)
	if err != nil {
		return PredictResponse{}, fmt.Errorf("scoring the vector at %s: %w", anchor.Format(time.RFC3339), err)
	}
	calibrated := m.calibrator.Calibrate(raw)

	anchor = anchor.UTC()
	return PredictResponse{
		AnchorUTC:      anchor,
		WindowEndUTC:   anchor.Add(m.window),
		RawProb:        raw,
		CalibratedProb: calibrated,
		Threshold:      m.threshold,
		// At exactly the threshold it rains
		WillRain:     calibrated >= m.threshold,
		ModelVersion: m.VersionID,
	}, nil
}

// Missing accounts for the features this model could not fill from f at
// anchor, so the caller can report the gaps a prediction was made over.
//
// Separate from Predict rather than returned by it: the forecast is cached, so
// what is missing changes when the fetch does and not when a request arrives.
func (m *Model) Missing(f *forecast.Forecast, anchor time.Time) (features.Missing, error) {
	return m.spec.Missing(f, anchor)
}

// AbsentColumns names the columns this model reads and f does not carry. The
// anchor-free half of Missing, for the startup check.
func (m *Model) AbsentColumns(f *forecast.Forecast) []string {
	return m.spec.AbsentColumns(f)
}

// decision is serving.json's decision half — the part neither
// internal/features nor internal/predict reads.
type decision struct {
	threshold float64
	window    time.Duration
}

// servingDecision is how those two fields arrive.
//
// Both are pointers so an artefact that omits one fails here rather than
// defaulting silently: a zero threshold calls rain on every hour of the year,
// and a zero window reports a prediction covering no time at all.
type servingDecision struct {
	Threshold             *float64 `json:"threshold"`
	PredictionWindowHours *int     `json:"prediction_window_hours"`
}

func parseDecision(servingJSON []byte) (decision, error) {
	var parsed servingDecision
	if err := json.Unmarshal(servingJSON, &parsed); err != nil {
		return decision{}, fmt.Errorf("parsing serving.json: %w", err)
	}

	if parsed.Threshold == nil {
		return decision{}, errors.New("serving.json names no threshold")
	}
	// It is compared against a calibrated probability, so a threshold outside
	// [0, 1] decides every hour the same way whatever the model says.
	if t := *parsed.Threshold; math.IsNaN(t) || t < 0 || t > 1 {
		return decision{}, fmt.Errorf("serving.json threshold is %g, want a probability in [0, 1]", t)
	}

	if parsed.PredictionWindowHours == nil {
		return decision{}, errors.New("serving.json names no prediction_window_hours")
	}
	if h := *parsed.PredictionWindowHours; h <= 0 {
		return decision{}, fmt.Errorf(
			"serving.json prediction_window_hours is %d, want a positive number of hours", h,
		)
	}

	return decision{
		threshold: *parsed.Threshold,
		window:    time.Duration(*parsed.PredictionWindowHours) * time.Hour,
	}, nil
}
