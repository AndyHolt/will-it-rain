package registry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testParent      = "projects/will-it-rain-496308/locations/europe-west2/models/1234567890"
	testArtifactURI = "gs://will-it-rain-496308-models/models/20260809T233603Z"
)

// newTestClient points a Client at a stub Vertex endpoint. It builds the
// struct directly rather than going through New, which would need ADC.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	cfg := Config{Location: "europe-west2"}
	if err := cfg.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults: %v", err)
	}
	return &Client{
		http:          server.Client(),
		project:       "will-it-rain-496308",
		cfg:           cfg,
		vertexBaseURL: server.URL + "/v1",
	}
}

// vertexStub answers the two calls ResolveProduction makes. Either body may be
// replaced with an empty string to make that call 404 instead.
func vertexStub(t *testing.T, listBody, versionBody string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		var body string
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			if got := r.URL.Query().Get("filter"); got != `display_name="will-it-rain"` {
				t.Errorf("filter = %q, want display_name=\"will-it-rain\"", got)
			}
			body = listBody
		case strings.HasSuffix(r.URL.Path, "@production"):
			if want := "/v1/" + testParent + "@production"; r.URL.Path != want {
				t.Errorf("path = %q, want %q", r.URL.Path, want)
			}
			body = versionBody
		default:
			t.Errorf("unexpected request to %q", r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
			return
		}

		if body == "" {
			http.Error(w, `{"error":{"code":404,"message":"not found"}}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("writing stub response: %v", err)
		}
	}
}

func listResponse(name string) string {
	return `{"models":[{"name":"` + name + `","displayName":"will-it-rain"}]}`
}

func versionResponse(name, versionID, artifactURI string) string {
	return `{"name":"` + name + `","versionId":"` + versionID +
		`","artifactUri":"` + artifactURI + `","versionAliases":["production"]}`
}

func TestResolveProduction(t *testing.T) {
	client := newTestClient(t, vertexStub(t,
		listResponse(testParent),
		versionResponse(testParent, "3", testArtifactURI),
	))

	got, err := client.ResolveProduction(context.Background())
	if err != nil {
		t.Fatalf("ResolveProduction: %v", err)
	}

	want := ProductionModel{ResourceName: testParent, VersionID: "3", ArtifactURI: testArtifactURI}
	if got != want {
		t.Errorf("ResolveProduction() = %+v, want %+v", got, want)
	}
}

// Both calls accept a versioned resource name in the `name` field. Carrying an
// "@1" through to the alias lookup would build "…@1@production" and 404.
func TestResolveProductionStripsVersionSuffix(t *testing.T) {
	client := newTestClient(t, vertexStub(t,
		listResponse(testParent+"@1"),
		versionResponse(testParent+"@3", "3", testArtifactURI),
	))

	got, err := client.ResolveProduction(context.Background())
	if err != nil {
		t.Fatalf("ResolveProduction: %v", err)
	}
	if got.ResourceName != testParent {
		t.Errorf("ResourceName = %q, want %q", got.ResourceName, testParent)
	}
}

func TestResolveProductionNoProductionModel(t *testing.T) {
	tests := []struct {
		name        string
		listBody    string
		versionBody string
	}{
		{
			name:        "nothing registered under the display name",
			listBody:    `{}`,
			versionBody: versionResponse(testParent, "3", testArtifactURI),
		},
		{
			name:     "registered but never promoted",
			listBody: listResponse(testParent),
			// Empty body makes the alias lookup 404.
			versionBody: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newTestClient(t, vertexStub(t, tt.listBody, tt.versionBody))

			_, err := client.ResolveProduction(context.Background())
			if !errors.Is(err, ErrNoProductionModel) {
				t.Fatalf("error = %v, want ErrNoProductionModel", err)
			}
		})
	}
}

// A non-gs:// artifactUri is a broken publish, not an unpromoted model, so it
// must not be swallowed as the benign first-run state.
func TestResolveProductionRejectsNonGCSArtifactURI(t *testing.T) {
	client := newTestClient(t, vertexStub(t,
		listResponse(testParent),
		versionResponse(testParent, "3", "https://example.invalid/model"),
	))

	_, err := client.ResolveProduction(context.Background())
	if err == nil {
		t.Fatal("ResolveProduction succeeded with a non-gs:// artifactUri, want error")
	}
	if errors.Is(err, ErrNoProductionModel) {
		t.Errorf("error = %v, want a hard failure rather than ErrNoProductionModel", err)
	}
}

func TestResolveProductionSurfacesHTTPStatus(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"caller lacks aiplatform.models.list"}}`, http.StatusForbidden)
	})

	_, err := client.ResolveProduction(context.Background())
	if err == nil {
		t.Fatal("ResolveProduction succeeded against a 403, want error")
	}
	if errors.Is(err, ErrNoProductionModel) {
		t.Fatalf("error = %v, want a hard failure rather than ErrNoProductionModel", err)
	}
	// The status and the server's explanation are what make a permissions
	// failure diagnosable from logs alone.
	for _, want := range []string{"403", "aiplatform.models.list"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestNewRequiresLocation(t *testing.T) {
	// Config is validated ahead of credentials, so this needs no ADC.
	_, err := New(context.Background(), Config{})
	if err == nil {
		t.Fatal("New succeeded without a location, want error")
	}
	if !strings.Contains(err.Error(), "LOCATION") {
		t.Errorf("error %q does not mention LOCATION", err)
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := Config{Location: "europe-west2"}
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
