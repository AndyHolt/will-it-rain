package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// recorded is the default policy with the waiting replaced by a note of what
// was asked for: the tests then assert the backoff instead of spending it.
func recorded(delays *[]time.Duration) RetryPolicy {
	policy := DefaultRetryPolicy()
	policy.wait = func(_ context.Context, d time.Duration) error {
		*delays = append(*delays, d)
		return nil
	}
	return policy
}

// countingServer stubs an endpoint that counts what it was asked. The handler
// receives the number of this request, starting at 1.
func countingServer(t *testing.T, handler func(http.ResponseWriter, int)) (*http.Client, string, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	client, url := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		handler(w, int(requests.Add(1)))
	})
	return client, url, &requests
}

// The reason the retry exists: Cloud Run scales to zero, so a blip against
// Open-Meteo or GCS during a cold start would otherwise fail the startup that
// provoked it.
func TestGetRetriesUntilItSucceeds(t *testing.T) {
	const body = "tree\nversion=v4\n"
	client, url, requests := countingServer(t, func(w http.ResponseWriter, n int) {
		if n < 3 {
			http.Error(w, "upstream connect error", http.StatusBadGateway)
			return
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("writing stub response: %v", err)
		}
	})

	var delays []time.Duration
	got, err := GetWithRetryPolicy(context.Background(), client, url, recorded(&delays))
	if err != nil {
		t.Fatalf("GetWithRetryPolicy: %v", err)
	}
	if string(got) != body {
		t.Errorf("GetWithRetryPolicy() = %q, want %q", got, body)
	}
	if requests.Load() != 3 {
		t.Errorf("made %d requests, want 3", requests.Load())
	}
	if len(delays) != 2 {
		t.Errorf("waited %d times between 3 attempts, want 2", len(delays))
	}
}

// Retrying these would delay the same failure rather than avoid it, and the
// 404 case is load-bearing: registry/vertex.go reads it as "never promoted".
func TestGetDoesNotRetryFailuresThatWillNotHeal(t *testing.T) {
	tests := []struct {
		name string
		code int
	}{
		{name: "no such object", code: http.StatusNotFound},
		{name: "missing grant", code: http.StatusForbidden},
		{name: "malformed request", code: http.StatusBadRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, url, requests := countingServer(t, func(w http.ResponseWriter, _ int) {
				http.Error(w, "no", test.code)
			})

			var delays []time.Duration
			_, err := GetWithRetryPolicy(context.Background(), client, url, recorded(&delays))

			var status *StatusError
			if !errors.As(err, &status) {
				t.Fatalf("error %v is not a *StatusError", err)
			}
			if status.Code != test.code {
				t.Errorf("Code = %d, want %d", status.Code, test.code)
			}
			if requests.Load() != 1 {
				t.Errorf("made %d requests, want 1", requests.Load())
			}
			if len(delays) != 0 {
				t.Errorf("waited %v before giving up, want no wait", delays)
			}
		})
	}
}

func TestGetGivesUpAfterTheAttemptBudget(t *testing.T) {
	client, url, requests := countingServer(t, func(w http.ResponseWriter, _ int) {
		http.Error(w, "still down", http.StatusServiceUnavailable)
	})

	var delays []time.Duration
	policy := recorded(&delays)
	_, err := GetWithRetryPolicy(context.Background(), client, url, policy)

	if err == nil {
		t.Fatal("GetWithRetryPolicy succeeded against a permanently failing endpoint, want error")
	}
	if got := int(requests.Load()); got != policy.Attempts {
		t.Errorf("made %d requests, want %d", got, policy.Attempts)
	}
	// The status has to survive the retrying: a dependency that is down and
	// one that is refusing the caller need different fixes.
	var status *StatusError
	if !errors.As(err, &status) {
		t.Fatalf("error %v is not a *StatusError", err)
	}
	// A log line saying only "503" cannot distinguish one blip from an outage
	// lasting the whole startup.
	if !strings.Contains(err.Error(), "attempts") {
		t.Errorf("error %q does not say it was retried", err)
	}
}

// Growing, and jittered within the window so that instances restarting
// together do not retry in lockstep.
func TestGetBacksOffProgressively(t *testing.T) {
	client, url, _ := countingServer(t, func(w http.ResponseWriter, _ int) {
		http.Error(w, "still down", http.StatusServiceUnavailable)
	})

	var delays []time.Duration
	policy := recorded(&delays)
	if _, err := GetWithRetryPolicy(context.Background(), client, url, policy); err == nil {
		t.Fatal("GetWithRetryPolicy succeeded against a permanently failing endpoint, want error")
	}

	if len(delays) != policy.Attempts-1 {
		t.Fatalf("waited %d times over %d attempts, want %d", len(delays), policy.Attempts, policy.Attempts-1)
	}
	for i, delay := range delays {
		window := min(policy.BaseDelay<<i, policy.MaxDelay)
		if delay < window/2 || delay > window {
			t.Errorf("delay %d = %v, want within [%v, %v]", i, delay, window/2, window)
		}
	}
}

// Open-Meteo publishes rate limits and answers 429 when one is hit. Guessing
// a backoff there ignores an answer the server has already given.
func TestGetHonoursRetryAfter(t *testing.T) {
	client, url, _ := countingServer(t, func(w http.ResponseWriter, n int) {
		if n == 1 {
			w.Header().Set("Retry-After", "2")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		if _, err := w.Write([]byte("ok")); err != nil {
			t.Errorf("writing stub response: %v", err)
		}
	})

	var delays []time.Duration
	if _, err := GetWithRetryPolicy(context.Background(), client, url, recorded(&delays)); err != nil {
		t.Fatalf("GetWithRetryPolicy: %v", err)
	}

	if len(delays) != 1 {
		t.Fatalf("waited %d times, want 1", len(delays))
	}
	if delays[0] != 2*time.Second {
		t.Errorf("waited %v, want the 2s the server asked for", delays[0])
	}
}

// A rate limit measured in minutes is not something to block a cold start on.
func TestGetStopsWhenRetryAfterExceedsTheBudget(t *testing.T) {
	client, url, requests := countingServer(t, func(w http.ResponseWriter, _ int) {
		w.Header().Set("Retry-After", "600")
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	})

	var delays []time.Duration
	_, err := GetWithRetryPolicy(context.Background(), client, url, recorded(&delays))

	var status *StatusError
	if !errors.As(err, &status) {
		t.Fatalf("error %v is not a *StatusError", err)
	}
	if status.Code != http.StatusTooManyRequests {
		t.Errorf("Code = %d, want %d", status.Code, http.StatusTooManyRequests)
	}
	if requests.Load() != 1 {
		t.Errorf("made %d requests, want 1", requests.Load())
	}
	if len(delays) != 0 {
		t.Errorf("waited %v, want no wait at all", delays)
	}
}

// Waiting past the caller's deadline spends the delay to arrive at a timeout.
// Giving up early keeps the answer that says what actually went wrong.
func TestGetStopsRatherThanWaitPastTheDeadline(t *testing.T) {
	client, url, requests := countingServer(t, func(w http.ResponseWriter, _ int) {
		http.Error(w, "still down", http.StatusServiceUnavailable)
	})

	// A backoff far longer than the deadline, so the decision is never a
	// close-run thing on a loaded machine.
	var delays []time.Duration
	policy := recorded(&delays)
	policy.BaseDelay, policy.MaxDelay = time.Minute, time.Minute

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := GetWithRetryPolicy(ctx, client, url, policy)

	var status *StatusError
	if !errors.As(err, &status) {
		t.Fatalf("error %v is not a *StatusError — the context error displaced it", err)
	}
	if requests.Load() != 1 {
		t.Errorf("made %d requests, want 1", requests.Load())
	}
	if len(delays) != 0 {
		t.Errorf("waited %v with %v of budget left, want no wait", delays, time.Second)
	}
}

// A connection that never completes is the transient case the status codes
// cannot describe.
func TestGetRetriesConnectionFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client, url := server.Client(), server.URL
	server.Close()

	var delays []time.Duration
	policy := recorded(&delays)
	_, err := GetWithRetryPolicy(context.Background(), client, url, policy)

	if err == nil {
		t.Fatal("GetWithRetryPolicy succeeded against a closed server, want error")
	}
	var status *StatusError
	if errors.As(err, &status) {
		t.Fatalf("error %v is a *StatusError, want the transport failure", err)
	}
	if len(delays) != policy.Attempts-1 {
		t.Errorf("waited %d times, want %d", len(delays), policy.Attempts-1)
	}
}

// The tests above replace the waiting to keep themselves quick, which leaves
// the wait itself — the one place a retry can outlast the caller — asserted
// nowhere else.
func TestSleepIsInterruptible(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if err := sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("returned after %v, want promptly", waited)
	}

	if err := sleep(context.Background(), time.Millisecond); err != nil {
		t.Errorf("sleep: %v", err)
	}
}

// A budget that runs out mid-wait has to report both halves: the caller's
// deadline is why the retrying stopped, and the status is what it was for.
func TestGetReportsBothTheDeadlineAndTheFailure(t *testing.T) {
	client, url, _ := countingServer(t, func(w http.ResponseWriter, _ int) {
		http.Error(w, "still down", http.StatusServiceUnavailable)
	})

	policy := DefaultRetryPolicy()
	policy.wait = func(context.Context, time.Duration) error {
		return context.DeadlineExceeded
	}

	_, err := GetWithRetryPolicy(context.Background(), client, url, policy)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v does not report the expired budget", err)
	}
	var status *StatusError
	if !errors.As(err, &status) {
		t.Errorf("error %v does not carry the status that was being retried", err)
	}
}

// The policy is exported, so a caller can hand over one they built themselves.
// The zero value has to mean something safe: one request, no waiting.
func TestGetWithRetryPolicyNormalisesAnUnfilledPolicy(t *testing.T) {
	client, url, requests := countingServer(t, func(w http.ResponseWriter, _ int) {
		http.Error(w, "still down", http.StatusServiceUnavailable)
	})

	if _, err := GetWithRetryPolicy(context.Background(), client, url, RetryPolicy{}); err == nil {
		t.Fatal("GetWithRetryPolicy succeeded against a failing endpoint, want error")
	}
	if requests.Load() != 1 {
		t.Errorf("made %d requests, want 1", requests.Load())
	}
}
