package httpx

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// Retrying: the policy, the loop that applies it, and the decisions it rests
// on. The loop takes the attempt to repeat as a function rather than making the
// request itself, which is what lets it live here — this file needs to know
// nothing about how a request is made, and httpx.go supplies that.

// RetryPolicy bounds how hard a request tries before giving up. Build one by
// adjusting DefaultRetryPolicy rather than from scratch, so that a field added
// here is picked up rather than left at zero.
type RetryPolicy struct {
	// Attempts counts the first try, so 1 disables retrying. Anything below 1
	// is read as 1 — a policy that was never filled in makes one request
	// rather than none.
	Attempts int

	// BaseDelay is the backoff window before the first retry; it doubles per
	// attempt up to MaxDelay. The actual wait is jittered within the window,
	// so that instances restarting together do not retry in lockstep.
	BaseDelay time.Duration
	MaxDelay  time.Duration

	// MaxRetryAfter is the longest Retry-After the caller will sit out. A
	// rate limit measured in minutes is not something to block a cold start
	// on: fail, and let the request that provoked it be retried by whoever
	// asked.
	MaxRetryAfter time.Duration

	// wait spends a delay, sleeping it when nil. Unexported because it is a
	// seam for this package's own tests — which assert what a policy asked
	// for rather than paying for it — and not a knob a caller has a use for.
	wait func(context.Context, time.Duration) error
}

// DefaultRetryPolicy is what Get uses. Deliberately short: it buys resilience
// to a blip, not to an outage, and both callers run at startup, where the
// budget is a cold start. A real rate limit is handled by Retry-After.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		Attempts:      4,
		BaseDelay:     100 * time.Millisecond,
		MaxDelay:      time.Second,
		MaxRetryAfter: 30 * time.Second,
	}
}

// attempt is one try at whatever is being retried: the body it produced, any
// delay the far end itself asked for — a Retry-After, when the attempt is an
// HTTP request — and the failure, if it failed.
//
// It is called repeatedly, so it has to be safe to call more than once. For a
// request that carries a body, that means rewinding it (http.NewRequest fills
// in req.GetBody for the bytes and strings readers, and cannot for an
// arbitrary io.Reader) and meaning it: a transport error says the response was
// lost, not that the server ignored the request.
type attempt func() ([]byte, time.Duration, error)

// retrying calls try under policy until one attempt succeeds, the attempts run
// out, or the caller's context does.
func retrying(ctx context.Context, policy RetryPolicy, try attempt) ([]byte, error) {
	// Normalised rather than rejected: a hand-built policy is still a usable
	// one, and the zero value should make one attempt rather than an error.
	if policy.Attempts < 1 {
		policy.Attempts = 1
	}
	if policy.wait == nil {
		policy.wait = sleep
	}

	for n := 1; ; n++ {
		body, retryAfter, err := try()
		if err == nil {
			return body, nil
		}

		// A cancelled context is the caller withdrawing the request, not a
		// failure that another attempt could fix.
		if ctx.Err() != nil {
			return nil, err
		}
		if n >= policy.Attempts || !retryable(err) {
			return nil, attempted(n, err)
		}

		delay := policy.delay(n, retryAfter)
		// Never wait past the caller's deadline: the wait would be spent to
		// reach a timeout, and the status that provoked it says more about
		// what went wrong than a context error would.
		if delay < 0 || !fits(ctx, delay) {
			return nil, attempted(n, err)
		}
		if waitErr := policy.wait(ctx, delay); waitErr != nil {
			// Both halves matter: the wait ended because the caller's budget
			// did, and the status is what the waiting was for.
			return nil, fmt.Errorf("%w while retrying: %w", waitErr, attempted(n, err))
		}
	}
}

// retryable reports whether another attempt could plausibly succeed.
func retryable(err error) bool {
	var status *StatusError
	if errors.As(err, &status) {
		return status.Code == http.StatusTooManyRequests || status.Code >= 500
	}
	// Everything else reaching here is transport-level: a refused connection,
	// a reset mid-body, a TLS handshake that timed out. A few of those never
	// heal — a bad certificate, say — but they are indistinguishable from the
	// transient ones without matching on error strings, and the cost of
	// retrying one is under a second.
	return true
}

// attempted names the retrying in the error, so that a log line distinguishes
// a dependency that was down throughout from one that failed once. Left alone
// when nothing was retried, which keeps the common failure message short.
func attempted(attempts int, err error) error {
	if attempts == 1 {
		return err
	}
	return fmt.Errorf("after %d attempts: %w", attempts, err)
}

// delay returns how long to wait before the attempt after this one, or -1 if
// the server asked for longer than this policy will wait.
func (p RetryPolicy) delay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > p.MaxRetryAfter {
			return -1
		}
		return retryAfter
	}

	window := min(p.BaseDelay<<(attempt-1), p.MaxDelay)
	// Jittered across the top half of the window: enough spread to break up
	// simultaneous restarts, while still growing attempt on attempt.
	return window/2 + rand.N(window/2+1)
}

// retryAfter reads the header, in its delay-seconds form. The HTTP-date form
// is equally legal but needs a clock to interpret, and neither API sends one;
// ignoring it falls back to the backoff, which is a shorter wait rather than a
// wrong one.
func retryAfter(header http.Header) time.Duration {
	seconds, err := strconv.Atoi(header.Get("Retry-After"))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// fits reports whether the context has room for a delay of d.
func fits(ctx context.Context, d time.Duration) bool {
	deadline, ok := ctx.Deadline()
	return !ok || time.Until(deadline) > d
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
