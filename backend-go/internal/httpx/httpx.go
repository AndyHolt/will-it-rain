package httpx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GETs endpoint using client and returns the whole body. Buffering releases the
// connection before the caller does anything with the bytes.
//
// Transient failures (a connection that did not complete, a 429, a 5xx) are
// retried with jittered exponential backoff, honouring Retry-After when the
// server sends one. Nothing else is: a 404 or a 403 is a wrong URL, a missing
// object or a missing grant, and will read the same on the fourth attempt as
// on the first.
//
// Anything other than 200 is returned as a *StatusError, so callers that care
// about a particular code can errors.As for it.
func Get(ctx context.Context, client *http.Client, endpoint string) ([]byte, error) {
	return GetWithRetryPolicy(ctx, client, endpoint, DefaultRetryPolicy())
}

// GetWithRetryPolicy is Get under a caller-chosen budget.
func GetWithRetryPolicy(
	ctx context.Context, client *http.Client, endpoint string, policy RetryPolicy,
) ([]byte, error) {
	// Built once and re-sent as it stands between attempts, which a GET can
	// be: there is no body to rewind and no state on the server to disturb.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	return retrying(ctx, policy, func() ([]byte, time.Duration, error) {
		return fetch(client, req)
	})
}

// fetch makes one attempt, returning the body on success and otherwise the
// failure plus any Retry-After the server asked for.
func fetch(client *http.Client, req *http.Request) ([]byte, time.Duration, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	// Not checked: the body is read-only here, so a Close error carries no
	// information the request itself has not already reported.
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		return nil, retryAfter(resp.Header), &StatusError{
			Code: resp.StatusCode,
			Body: strings.TrimSpace(string(body)),
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("reading response: %w", err)
	}
	return body, 0, nil
}
