package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// noRetry isolates a single attempt, for the tests that are about what one
// response becomes rather than about how many are made.
var noRetry = RetryPolicy{Attempts: 1}

// newTestServer stubs an endpoint and returns its URL and a client for it.
func newTestServer(t *testing.T, handler http.HandlerFunc) (*http.Client, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server.Client(), server.URL
}

// Every API client in the service shares this transport, so what a failure
// reports is decided here rather than separately in each of them.
func TestGetReportsStatusAndBody(t *testing.T) {
	client, url := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"caller lacks storage.objects.get"}}`, http.StatusForbidden)
	})

	_, err := Get(context.Background(), client, url)
	if err == nil {
		t.Fatal("Get succeeded against a 403, want error")
	}

	// registry/vertex.go inspects the code to tell "never promoted" from a
	// real failure, so this has to survive as a typed error.
	var status *StatusError
	if !errors.As(err, &status) {
		t.Fatalf("error %v is not a *StatusError", err)
	}
	if status.Code != http.StatusForbidden {
		t.Errorf("Code = %d, want %d", status.Code, http.StatusForbidden)
	}
	// Without the body, a permissions failure and a wrong URL look alike.
	if !strings.Contains(status.Body, "storage.objects.get") {
		t.Errorf("Body = %q, does not carry the server's explanation", status.Body)
	}
}

func TestGetReturnsBodyVerbatim(t *testing.T) {
	// Bodies are not all JSON: model.txt is LightGBM's own text format and has
	// to arrive byte-for-byte for leaves to parse it, and the Open-Meteo
	// forecast is FlatBuffers.
	const body = "tree\nversion=v4\nnum_class=1\n"

	client, url := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("writing stub response: %v", err)
		}
	})

	got, err := Get(context.Background(), client, url)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != body {
		t.Errorf("Get() = %q, want %q", got, body)
	}
}

func TestGetTruncatesLargeErrorBody(t *testing.T) {
	// A proxy or auth failure can answer with an arbitrarily large HTML page,
	// which must not end up whole in an error string.
	client, url := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		if _, err := w.Write([]byte(strings.Repeat("x", errorBodyLimit*4))); err != nil {
			t.Errorf("writing stub response: %v", err)
		}
	})

	// A 502 is retryable, and this test is about the body rather than the
	// retrying, so it goes through the policy that does neither.
	_, err := GetWithRetryPolicy(context.Background(), client, url, noRetry)
	if err == nil {
		t.Fatal("Get succeeded against a 502, want error")
	}
	var status *StatusError
	if !errors.As(err, &status) {
		t.Fatalf("error %v is not a *StatusError", err)
	}
	if len(status.Body) > errorBodyLimit {
		t.Errorf("Body is %d bytes, want no more than %d", len(status.Body), errorBodyLimit)
	}
}

func TestGetHonoursContextCancellation(t *testing.T) {
	// The caller's context is the real request budget; Client.Timeout in each
	// package is only a backstop.
	client, url := newTestServer(t, func(_ http.ResponseWriter, _ *http.Request) {})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Get(ctx, client, url); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}
