package server

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/AndyHolt/will-it-rain/backend/internal/registry"
)

// TestNewModelRejectsAnUnservableDecision covers the half of serving.json this
// package parses. Each case is a live artefact under one edit, and each would
// otherwise reach a caller as a confident answer rather than as a failure.
func TestNewModelRejectsAnUnservableDecision(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		value   any
		wantErr string
	}{
		{"no threshold", "threshold", nil, "names no threshold"},
		{"threshold above 1", "threshold", 1.5, "want a probability"},
		{"threshold below 0", "threshold", -0.1, "want a probability"},
		{"no window", "prediction_window_hours", nil, "names no prediction_window_hours"},
		{"zero window", "prediction_window_hours", 0, "want a positive number of hours"},
		{"negative window", "prediction_window_hours", -4, "want a positive number of hours"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			champion := goldenChampion(t)
			champion.ServingJSON = servingJSONWith(t, tc.key, tc.value)

			_, err := NewModel(champion, time.Now())
			if err == nil {
				t.Fatalf("NewModel accepted %s=%v", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestNewModelRejectsUnreadableArtefacts pins that a Champion whose files are
// not what they claim to be fails at startup, where the version that published
// them is still to hand.
func TestNewModelRejectsUnreadableArtefacts(t *testing.T) {
	t.Run("serving.json", func(t *testing.T) {
		champion := goldenChampion(t)
		champion.ServingJSON = []byte("not json")
		if _, err := NewModel(champion, time.Now()); err == nil {
			t.Fatal("NewModel accepted a serving.json that is not JSON")
		}
	})

	t.Run("model.txt", func(t *testing.T) {
		champion := goldenChampion(t)
		champion.ModelText = []byte("not a booster")
		if _, err := NewModel(champion, time.Now()); err == nil {
			t.Fatal("NewModel accepted a model.txt that is not a booster")
		}
	})
}

// TestWillRainIncludesTheThreshold pins the comparison rather than the value:
// shared/predict.py calls rain at calibrated >= threshold, so a probability
// landing exactly on it rains. Every other test here sits well clear of the
// boundary, which is where a > would hide.
func TestWillRainIncludesTheThreshold(t *testing.T) {
	expected := readExpected(t)
	current := expected.Forecast.build(t)
	now := parseTime(t, expected.NowUTC)

	// The threshold to sit exactly on is the calibrated probability this model
	// actually produces, so read it off a prediction rather than assuming the
	// fixture's value to the last bit.
	baseline, err := loadGoldenModel(t, now).Predict(current, now)
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}

	cases := []struct {
		name      string
		threshold float64
		want      bool
	}{
		{"exactly at the threshold", baseline.CalibratedProb, true},
		{"just above it", math.Nextafter(baseline.CalibratedProb, 1), false},
		{"just below it", math.Nextafter(baseline.CalibratedProb, 0), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			champion := goldenChampion(t)
			champion.ServingJSON = servingJSONWith(t, "threshold", tc.threshold)
			model, err := NewModel(champion, now)
			if err != nil {
				t.Fatalf("NewModel: %v", err)
			}

			got, err := model.Predict(current, now)
			if err != nil {
				t.Fatalf("Predict: %v", err)
			}
			if got.WillRain != tc.want {
				t.Errorf("will_rain = %t at threshold %.17g, want %t",
					got.WillRain, tc.threshold, tc.want)
			}
		})
	}
}

// TestPredictWindowFollowsTheSpec pins that the window is read from
// serving.json rather than hardcoded, which is the whole reason
// save_serving_artefacts writes it: PREDICTION_WINDOW_HOURS is the label
// horizon the model was trained against, and a window this service made up
// would describe a different question from the one it answers.
func TestPredictWindowFollowsTheSpec(t *testing.T) {
	expected := readExpected(t)
	current := expected.Forecast.build(t)
	now := parseTime(t, expected.NowUTC)

	champion := goldenChampion(t)
	champion.ServingJSON = servingJSONWith(t, "prediction_window_hours", 7)
	model, err := NewModel(champion, now)
	if err != nil {
		t.Fatalf("NewModel: %v", err)
	}

	got, err := model.Predict(current, now)
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if want := got.AnchorUTC.Add(7 * time.Hour); !got.WindowEndUTC.Equal(want) {
		t.Errorf("window_end_utc = %s, want %s", got.WindowEndUTC, want)
	}
}

// TestNewModelReportsLoadTimeInUTC keeps /api/health's loaded_at_utc honest:
// the field says UTC, and a Time carrying any other zone would serialise with
// an offset the name denies.
func TestNewModelReportsLoadTimeInUTC(t *testing.T) {
	zone := time.FixedZone("BST", 3600)
	loadedAt := time.Date(2026, 8, 19, 21, 15, 0, 0, zone)

	model, err := NewModel(goldenChampion(t), loadedAt)
	if err != nil {
		t.Fatalf("NewModel: %v", err)
	}
	if got := model.LoadedAt.Location(); got != time.UTC {
		t.Errorf("LoadedAt is in %s, want UTC", got)
	}
	if !model.LoadedAt.Equal(loadedAt) {
		t.Errorf("LoadedAt = %s, want the same instant as %s", model.LoadedAt, loadedAt)
	}
}

// TestNewModelCarriesTheRegistryMetadata pins that health reports what the
// registry said, not what the artefacts contain — the version and resource are
// the only part of a Champion the files know nothing about.
func TestNewModelCarriesTheRegistryMetadata(t *testing.T) {
	champion := goldenChampion(t)
	champion.ProductionModel = registry.ProductionModel{
		ResourceName: "projects/p/locations/l/models/9",
		VersionID:    "42",
	}

	model, err := NewModel(champion, time.Now())
	if err != nil {
		t.Fatalf("NewModel: %v", err)
	}
	if model.VersionID != "42" || model.ResourceName != champion.ResourceName {
		t.Errorf("model reports %s/%s, want 42/%s",
			model.VersionID, model.ResourceName, champion.ResourceName)
	}
}
