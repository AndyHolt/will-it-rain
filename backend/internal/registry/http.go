package registry

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/AndyHolt/will-it-rain/backend/internal/httpx"
)

// getJSON GETs endpoint and decodes the body into out. Transport behaviour —
// including what a failed status turns into — lives in internal/httpx; this
// adds only the decode, which is Vertex-specific: the GCS artefact fetch in
// artefacts.go wants the bytes verbatim.
func (c *Client) getJSON(ctx context.Context, endpoint string, out any) error {
	body, err := httpx.Get(ctx, c.http, endpoint)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}
