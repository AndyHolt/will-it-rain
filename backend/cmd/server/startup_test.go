package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AndyHolt/will-it-rain/backend/internal/forecast"
)

// stubFetcher stands in for *forecast.Client, counting fetches so a warm-up
// that quietly does nothing is distinguishable from one that worked.
type stubFetcher struct {
	err     error
	delay   time.Duration
	fetches atomic.Int32
}

func (s *stubFetcher) Fetch(ctx context.Context) (*forecast.Forecast, error) {
	s.fetches.Add(1)
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	return &forecast.Forecast{}, nil
}

// captureLogs returns a logger and a function reading back what it recorded.
func captureLogs(t *testing.T) (*slog.Logger, func() []map[string]any) {
	t.Helper()
	var recorded strings.Builder
	logger := slog.New(slog.NewJSONHandler(&recorded, cloudLoggingOptions()))

	return logger, func() []map[string]any {
		var lines []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(recorded.String()), "\n") {
			if line == "" {
				continue
			}
			var fields map[string]any
			if err := json.Unmarshal([]byte(line), &fields); err != nil {
				t.Fatalf("decoding log line %q: %v", line, err)
			}
			lines = append(lines, fields)
		}
		return lines
	}
}

// TestWarmForecastFillsTheCache covers the ordinary path: one fetch made, and
// said so at info, which is the line the cold-start measurement in the plan's
// Phase 5 reads.
func TestWarmForecastFillsTheCache(t *testing.T) {
	logger, logs := captureLogs(t)
	fetcher := &stubFetcher{}

	warmed := warmForecast(context.Background(), fetcher, logger)

	if got := fetcher.fetches.Load(); got != 1 {
		t.Errorf("made %d fetches, want 1", got)
	}
	// Returned as well as cached, because the startup column check runs
	// against it before any request has one of its own.
	if warmed == nil {
		t.Error("warmForecast returned no forecast on the ordinary path")
	}
	if !loggedAt(logs(), "INFO", "warmed") {
		t.Errorf("no INFO line reporting the warm-up: %v", logs())
	}
}

// TestWarmForecastSurvivesAFailure is the behaviour that makes the warm-up
// safe to add to startup: Open-Meteo being unreachable costs the first request
// a fetch, not the instance its life. The signature carries the guarantee —
// there is no error to return — so what is left to pin is that it says so.
func TestWarmForecastSurvivesAFailure(t *testing.T) {
	logger, logs := captureLogs(t)
	fetcher := &stubFetcher{err: errors.New("open-meteo is down")}

	if warmed := warmForecast(context.Background(), fetcher, logger); warmed != nil {
		t.Errorf("warmForecast returned %v after a failed fetch, want nil", warmed)
	}

	if !loggedAt(logs(), "WARNING", "could not warm") {
		t.Errorf("no WARNING line reporting the failure: %v", logs())
	}
}

// TestWarmForecastIsQuietWhenAbandoned covers the failed-model-load path,
// where the startup context is cancelled out from under the fetch. The model
// error is what that startup is about; a warning about the forecast it
// cancelled would only compete with it.
func TestWarmForecastIsQuietWhenAbandoned(t *testing.T) {
	logger, logs := captureLogs(t)
	ctx, cancel := context.WithCancel(context.Background())
	fetcher := &stubFetcher{delay: time.Minute}

	go cancel()
	warmForecast(ctx, fetcher, logger)

	if lines := logs(); len(lines) != 0 {
		t.Errorf("abandoned warm-up logged %v", lines)
	}
}

// TestWarmForecastReportsATimeout separates a deadline from a cancellation:
// the model may well have loaded, and a first request that pays for a fetch
// anyway should be attributable to something.
func TestWarmForecastReportsATimeout(t *testing.T) {
	logger, logs := captureLogs(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	warmForecast(ctx, &stubFetcher{delay: time.Minute}, logger)

	if !loggedAt(logs(), "WARNING", "could not warm") {
		t.Errorf("no WARNING line reporting the timeout: %v", logs())
	}
}

// loggedAt reports whether any line was recorded at severity with a message
// starting the given way.
func loggedAt(lines []map[string]any, severity, message string) bool {
	for _, line := range lines {
		got, _ := line["message"].(string)
		if line["severity"] == severity && strings.HasPrefix(got, message) {
			return true
		}
	}
	return false
}
