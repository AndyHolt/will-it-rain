package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/oauth2"
)

const (
	defaultModelDisplayName = "will-it-rain"
	defaultProductionAlias  = "production"

	// GCS is not regional in its endpoint: one host serves every bucket, and
	// which bucket comes from the registry rather than from configuration.
	storageBaseURL = "https://storage.googleapis.com/storage/v1"

	// Backstop only — callers pass a context whose deadline is the real
	// budget. Without it a hung connection would hang startup indefinitely.
	requestTimeout = 20 * time.Second
)

// Config is the deployment-time part of resolving a model. The project is
// deliberately absent: ResolveCredentials answers it from the environment the
// service is running in.
type Config struct {
	// Region is the Vertex region, e.g. "europe-west2", populated from the
	// LOCATION env var that cloud_run.tf injects. Required — ADC carries a
	// project but never a region, so it cannot be discovered.
	Region string

	// ModelDisplayName defaults to "will-it-rain".
	ModelDisplayName string

	// ProductionAlias defaults to "production".
	ProductionAlias string
}

func (c *Config) applyDefaults() error {
	if c.Region == "" {
		return errors.New("registry.Config.Region is required: set LOCATION")
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

	// Both overridden in tests. A regional endpoint is required for Vertex:
	// the global host does not serve model resources.
	vertexBaseURL  string
	storageBaseURL string
}

// New resolves credentials and returns a Client addressing cfg.Region.
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
		http:           httpClient,
		project:        creds.ProjectID,
		cfg:            cfg,
		vertexBaseURL:  fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1", cfg.Region),
		storageBaseURL: storageBaseURL,
	}, nil
}
