package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient points a Client at a stub Vertex endpoint. It builds the
// struct directly rather than going through New, which would need ADC.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	cfg := Config{Region: "europe-west2"}
	if err := cfg.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults: %v", err)
	}
	return &Client{
		http:           server.Client(),
		project:        "will-it-rain-496308",
		cfg:            cfg,
		vertexBaseURL:  server.URL + "/v1",
		storageBaseURL: server.URL + "/storage/v1",
	}
}

func TestNewRequiresRegion(t *testing.T) {
	// Config is validated ahead of credentials, so this needs no ADC.
	_, err := New(context.Background(), Config{})
	if err == nil {
		t.Fatal("New succeeded without a region, want error")
	}
	// The message has to name the env var, not just the field: LOCATION is
	// what an operator sets.
	if !strings.Contains(err.Error(), "LOCATION") {
		t.Errorf("error %q does not mention LOCATION", err)
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := Config{Region: "europe-west2"}
	if err := cfg.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults: %v", err)
	}
	if cfg.ModelDisplayName != "will-it-rain" {
		t.Errorf("ModelDisplayName = %q, want %q", cfg.ModelDisplayName, "will-it-rain")
	}
	if cfg.ProductionAlias != "production" {
		t.Errorf("ProductionAlias = %q, want %q", cfg.ProductionAlias, "production")
	}
}
