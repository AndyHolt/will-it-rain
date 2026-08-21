package httpx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// errorBodyLimit is how much of a failed response body to quote back. The
// APIs called here answer with a short JSON reason, but a proxy or auth
// failure in front of one can return an arbitrarily large HTML page.
const errorBodyLimit = 2048

// StatusError is a response other than 200 OK, carrying enough of the body to
// tell an auth failure from a missing resource without turning on request
// logging. Neither API answers a success with any other 2xx, so the check is
// deliberately an equality rather than a range.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Code, e.Body)
}

// Get GETs endpoint using client and returns the whole body. Every response
// this service fetches is small — the largest is the ~156 KiB model — and
// buffering releases the connection before the caller does anything with the
// bytes.
//
// Anything other than 200 is returned as a *StatusError, so callers that care
// about a particular code can errors.As for it.
func Get(ctx context.Context, client *http.Client, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	// Not checked: the body is read-only here, so a Close error carries no
	// information the request itself has not already reported.
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		return nil, &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	return body, nil
}
