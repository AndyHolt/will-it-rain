package registry

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const (
	testParent      = "projects/will-it-rain-496308/locations/europe-west2/models/1234567890"
	testArtifactURI = "gs://will-it-rain-496308-models/models/20260809T233603Z"

	// What the list call reports: the artefacts of whichever version is
	// @default. Deliberately a later training run than testArtifactURI —
	// @production is routinely an older version than the newest registered.
	testListedArtifactURI = "gs://will-it-rain-496308-models/models/20260816T233558Z"
)

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
	return `{"models":[{"name":"` + name + `","displayName":"will-it-rain",` +
		`"versionId":"4","versionAliases":["default"],` +
		`"artifactUri":"` + testListedArtifactURI + `"}]}`
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

// The alias lookup answers with the name it was asked for, alias suffix and
// all — "…/models/1234567890@production" is the ordinary response, not an edge
// case. Carrying that (or an "@1" from the list call) into the next request
// would build "…@production@production" and 404.
func TestResolveProductionStripsVersionSuffix(t *testing.T) {
	client := newTestClient(t, vertexStub(t,
		listResponse(testParent+"@1"),
		versionResponse(testParent+"@production", "3", testArtifactURI),
	))

	got, err := client.ResolveProduction(context.Background())
	if err != nil {
		t.Fatalf("ResolveProduction: %v", err)
	}
	if got.ResourceName != testParent {
		t.Errorf("ResourceName = %q, want %q", got.ResourceName, testParent)
	}
}

// The listed model carries artefacts of its own, and they are the wrong ones:
// the list call finds the model, the alias call finds the version being served.
// Resolving the alias is the only reason for the second round trip, so pin that
// the artefacts come from it.
func TestResolveProductionPrefersTheAliasedVersionsArtefacts(t *testing.T) {
	client := newTestClient(t, vertexStub(t,
		listResponse(testParent),
		versionResponse(testParent, "3", testArtifactURI),
	))

	got, err := client.ResolveProduction(context.Background())
	if err != nil {
		t.Fatalf("ResolveProduction: %v", err)
	}
	if got.ArtifactURI != testArtifactURI {
		t.Errorf(
			"ArtifactURI = %q, want @production's %q",
			got.ArtifactURI, testArtifactURI,
		)
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
