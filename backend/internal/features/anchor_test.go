package features

import (
	"testing"
	"time"
)

// The fixture's now_utc is half past the hour the capture ran in, so this pins
// both halves of the rule at once: flooring, and then selecting the latest
// forecast hour at or before it. Trusting a hardcoded index instead would let
// an off-by-one anchor pass and take all seventy features with it.
func TestPickAnchorMatchesTheGoldenAnchor(t *testing.T) {
	expected := readExpected(t)

	got, err := PickAnchor(expected.Forecast.build(t), parseTime(t, expected.NowUTC))
	if err != nil {
		t.Fatalf("PickAnchor: %v", err)
	}
	if want := parseTime(t, expected.AnchorUTC); !got.Equal(want) {
		t.Errorf("PickAnchor = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestPickAnchorSelectsTheLatestHourAtOrBeforeNow(t *testing.T) {
	start := parseTime(t, "2026-03-04T00:00:00Z")
	frame := hourly(start, 10, 11, 12)

	for _, test := range []struct {
		name string
		now  string
		want string
	}{
		{"exactly on a forecast hour", "2026-03-04T01:00:00Z", "2026-03-04T01:00:00Z"},
		{"part way through an hour", "2026-03-04T01:30:00Z", "2026-03-04T01:00:00Z"},
		{"a second before the next hour", "2026-03-04T01:59:59Z", "2026-03-04T01:00:00Z"},
		// Past the end of the frame the last hour is still the last completed
		// one, which is what a stale cached forecast looks like.
		{"past the end of the forecast", "2026-03-04T09:15:00Z", "2026-03-04T02:00:00Z"},
		// Not in UTC: the rule floors the instant, not the local wall clock.
		{"expressed in another zone", "2026-03-04T02:30:00+01:00", "2026-03-04T01:00:00Z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := PickAnchor(frame, parseTime(t, test.now))
			if err != nil {
				t.Fatalf("PickAnchor: %v", err)
			}
			if want := parseTime(t, test.want); !got.Equal(want) {
				t.Errorf("PickAnchor = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
			}
		})
	}
}

// Every hour lying ahead of now means the forecast is not one this service can
// serve from — there is no completed hour to anchor on, and picking the first
// future hour would predict a window that has not started.
func TestPickAnchorRejectsAForecastThatStartsAfterNow(t *testing.T) {
	frame := hourly(parseTime(t, "2026-03-04T06:00:00Z"), 10, 11)

	if _, err := PickAnchor(frame, parseTime(t, "2026-03-04T05:30:00Z")); err == nil {
		t.Error("PickAnchor accepted a forecast beginning after now")
	}
}

func TestPickAnchorRejectsAnEmptyForecast(t *testing.T) {
	if _, err := PickAnchor(frameOf(nil, nil), parseTime(t, "2026-03-04T05:30:00Z")); err == nil {
		t.Error("PickAnchor accepted a forecast with no hours")
	}
}
