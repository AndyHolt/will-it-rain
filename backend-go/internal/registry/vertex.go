package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/AndyHolt/will-it-rain/backend-go/internal/httpx"
)

// ErrNoProductionModel reports that the registry holds no version aliased
// @production — either nothing is registered under the display name at all, or
// the parent exists but has never been promoted.
//
// It is separate from a lookup *failure* because the two want different
// handling: this one is the legitimate first-run state, where the service
// still starts and serves 503 from /api/predict, matching the Python backend.
var ErrNoProductionModel = errors.New("no @production model")

// ProductionModel is what the registry knows about the version currently
// aliased @production: which version it is, and where its artefacts live.
type ProductionModel struct {
	// ResourceName is version-less ("projects/…/models/{id}"), matching what
	// the Python backend reports as model_resource on /api/health.
	ResourceName string
	VersionID    string
	ArtifactURI  string
}

// ResolveProduction returns the version currently aliased @production.
//
// Two round trips: display name → parent resource, then parent@alias → version.
// The first is unavoidable — there is no lookup-by-display-name endpoint that
// resolves an alias in one call, because the alias hangs off the parent
// resource name.
func (c *Client) ResolveProduction(ctx context.Context) (ProductionModel, error) {
	parent, err := c.parentResource(ctx)
	if err != nil {
		return ProductionModel{}, err
	}
	return c.aliasedVersion(ctx, parent)
}

// parentResource resolves the model display name to its parent resource name.
func (c *Client) parentResource(ctx context.Context) (string, error) {
	endpoint := fmt.Sprintf(
		"%s/projects/%s/locations/%s/models?%s",
		c.vertexBaseURL, c.project, c.cfg.Region,
		url.Values{"filter": {fmt.Sprintf("display_name=%q", c.cfg.ModelDisplayName)}}.Encode(),
	)

	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := c.getJSON(ctx, endpoint, &body); err != nil {
		return "", fmt.Errorf("listing models named %q: %w", c.cfg.ModelDisplayName, err)
	}

	if len(body.Models) == 0 {
		return "", fmt.Errorf(
			"nothing registered under display_name %q: %w",
			c.cfg.ModelDisplayName, ErrNoProductionModel,
		)
	}
	return stripVersion(body.Models[0].Name), nil
}

// aliasedVersion resolves parent@alias to the version it points at.
func (c *Client) aliasedVersion(ctx context.Context, parent string) (ProductionModel, error) {
	endpoint := fmt.Sprintf("%s/%s@%s", c.vertexBaseURL, parent, c.cfg.ProductionAlias)

	var body struct {
		Name        string `json:"name"`
		VersionID   string `json:"versionId"`
		ArtifactURI string `json:"artifactUri"`
	}
	if err := c.getJSON(ctx, endpoint, &body); err != nil {
		// The alias is a sub-resource of a model that does exist, so "never
		// promoted" arrives as a 404 rather than an empty result.
		var status *httpx.StatusError
		if errors.As(err, &status) && status.Code == http.StatusNotFound {
			return ProductionModel{}, fmt.Errorf(
				"%s has no @%s alias: %w",
				parent, c.cfg.ProductionAlias, ErrNoProductionModel,
			)
		}
		return ProductionModel{}, fmt.Errorf("resolving %s@%s: %w", parent, c.cfg.ProductionAlias, err)
	}

	// A URI that is present but not gs:// means a registry entry we cannot
	// load from — a broken publish rather than an unpromoted model, so it is
	// deliberately not ErrNoProductionModel.
	if !strings.HasPrefix(body.ArtifactURI, "gs://") {
		return ProductionModel{}, fmt.Errorf(
			"version %s of %s has artifactUri %q, want a gs:// URI",
			body.VersionID, parent, body.ArtifactURI,
		)
	}

	resourceName := stripVersion(body.Name)
	if resourceName == "" {
		resourceName = parent
	}
	return ProductionModel{
		ResourceName: resourceName,
		VersionID:    body.VersionID,
		ArtifactURI:  body.ArtifactURI,
	}, nil
}

// stripVersion drops any "@version" suffix from a model resource name.
// Vertex returns the version-less form in both calls here, but the versioned
// form is legal in the same field, and appending "@production" to a name that
// already carries "@3" would silently produce a 404.
func stripVersion(name string) string {
	base, _, _ := strings.Cut(name, "@")
	return base
}
