package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// How much of a failed response body to quote back in the error. Vertex and
// GCS error payloads are small, but a proxy or auth failure can return an
// arbitrarily large HTML page.
const errorBodyLimit = 2048

// statusError is a non-2xx response, carrying enough of the body to tell an
// auth failure from a missing resource without turning on request logging.
type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.code, e.body)
}

// getBytes GETs endpoint and returns the whole body. Both artefacts are small
// (the model is ~156 KiB), and buffering lets the connection be released
// before the caller does anything with the bytes.
func (c *Client) getBytes(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	// Not checked: the body is read-only here, so a Close error carries no
	// information the request itself has not already reported.
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		return nil, &statusError{code: resp.StatusCode, body: strings.TrimSpace(string(body))}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	return body, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, out any) error {
	body, err := c.getBytes(ctx, endpoint)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}
