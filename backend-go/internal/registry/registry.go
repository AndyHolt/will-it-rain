package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const (
	defaultModelDisplayName = "will-it-rain"
	defaultProductionAlias  = "production"

	// Backstop only — callers pass a context whose deadline is the real
	// budget. Without it a hung connection would hang startup indefinitely.
	requestTimeout = 20 * time.Second

	// How much of a failed response body to quote back in the error. Vertex
	// error payloads are small, but a proxy or auth failure can return an
	// arbitrarily large HTML page.
	errorBodyLimit = 2048
)

// ErrNoProductionModel reports that the registry holds no version aliased
// @production — either nothing is registered under the display name at all, or
// the parent exists but has never been promoted.
//
// It is separate from a lookup *failure* because the two want different
// handling: this one is the legitimate first-run state, where the service
// still starts and serves 503 from /api/predict, matching the Python backend.
var ErrNoProductionModel = errors.New("no @production model")

// Config is the deployment-time part of resolving a model. The project is
// deliberately absent: ResolveCredentials answers it from the environment the
// service is running in.
type Config struct {
	// Location is the Vertex region, e.g. "europe-west2". Required — ADC
	// carries a project but never a location, so it cannot be discovered.
	Location string

	// ModelDisplayName defaults to "will-it-rain".
	ModelDisplayName string

	// ProductionAlias defaults to "production".
	ProductionAlias string
}

func (c *Config) applyDefaults() error {
	if c.Location == "" {
		return errors.New("registry.Config.Location is required: set LOCATION")
	}
	if c.ModelDisplayName == "" {
		c.ModelDisplayName = defaultModelDisplayName
	}
	if c.ProductionAlias == "" {
		c.ProductionAlias = defaultProductionAlias
	}
	return nil
}

// Client talks to the Vertex Model Registry and to GCS on one credential.
type Client struct {
	http    *http.Client
	project string
	cfg     Config

	// Overridden in tests. Regional endpoints are required for Vertex: the
	// global host does not serve model resources.
	vertexBaseURL string
}

// New resolves credentials and returns a Client addressing cfg.Location.
//
// Config is validated before credentials are touched, so a missing LOCATION
// fails on its own terms rather than behind an authentication error.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}

	creds, err := ResolveCredentials(ctx)
	if err != nil {
		return nil, err
	}

	httpClient := oauth2.NewClient(ctx, creds.TokenSource)
	httpClient.Timeout = requestTimeout

	return &Client{
		http:          httpClient,
		project:       creds.ProjectID,
		cfg:           cfg,
		vertexBaseURL: fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1", cfg.Location),
	}, nil
}

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
		c.vertexBaseURL, c.project, c.cfg.Location,
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
		var status *statusError
		if errors.As(err, &status) && status.code == http.StatusNotFound {
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

// statusError is a non-2xx response, carrying enough of the body to tell an
// auth failure from a missing resource without turning on request logging.
type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.code, e.body)
}

func (c *Client) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	// Not checked: the body is read-only here, so a Close error carries no
	// information the request itself has not already reported.
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		return &statusError{code: resp.StatusCode, body: strings.TrimSpace(string(body))}
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}
