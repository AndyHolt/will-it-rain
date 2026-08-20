package registry

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const testBucket = "will-it-rain-496308-models"

// requestedObject returns the object name a GCS media request addresses, or ""
// if the request is not one. EscapedPath rather than Path: the object name is
// a single segment whose slashes arrive as %2F, and Path has already decoded
// them back into path separators.
func requestedObject(t *testing.T, r *http.Request) string {
	t.Helper()
	_, escaped, ok := strings.Cut(r.URL.EscapedPath(), "/storage/v1/b/"+testBucket+"/o/")
	if !ok {
		return ""
	}
	if got := r.URL.Query().Get("alt"); got != "media" {
		t.Errorf("alt = %q, want media (otherwise GCS returns object metadata)", got)
	}
	object, err := url.PathUnescape(escaped)
	if err != nil {
		t.Fatalf("unescaping object name %q: %v", escaped, err)
	}
	return object
}

// artefactStub serves the two Vertex calls plus whatever objects is keyed on.
// An object absent from objects 404s, as a version predating the serving
// contract would.
func artefactStub(t *testing.T, objects map[string]string, onObject func()) http.HandlerFunc {
	t.Helper()
	vertex := vertexStub(t,
		listResponse(testParent),
		versionResponse(testParent, "3", testArtifactURI),
	)
	return func(w http.ResponseWriter, r *http.Request) {
		object := requestedObject(t, r)
		if object == "" {
			vertex(w, r)
			return
		}
		if onObject != nil {
			onObject()
		}
		body, ok := objects[object]
		if !ok {
			http.Error(w, `{"error":{"code":404,"message":"No such object"}}`, http.StatusNotFound)
			return
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("writing stub object %q: %v", object, err)
		}
	}
}

// testArtifactURI is gs://<bucket>/models/<timestamp>, so the objects sit
// under that prefix — the same prefix register.py uploads them to.
const testPrefix = "models/20260809T233603Z/"

func TestLoad(t *testing.T) {
	client := newTestClient(t, artefactStub(t, map[string]string{
		testPrefix + "model.txt":    "tree\nversion=v4\n",
		testPrefix + "serving.json": `{"threshold":0.42}`,
	}, nil))

	champion, err := client.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if champion.VersionID != "3" {
		t.Errorf("VersionID = %q, want %q", champion.VersionID, "3")
	}
	if champion.ResourceName != testParent {
		t.Errorf("ResourceName = %q, want %q", champion.ResourceName, testParent)
	}
	if got := string(champion.ModelText); got != "tree\nversion=v4\n" {
		t.Errorf("ModelText = %q, want the model.txt body", got)
	}
	if got := string(champion.ServingJSON); got != `{"threshold":0.42}` {
		t.Errorf("ServingJSON = %q, want the serving.json body", got)
	}
}

// The two fetches are independent round trips on the cold-start path, so they
// have to overlap. Sequential fetching passes every other assertion here.
func TestLoadFetchesArtefactsConcurrently(t *testing.T) {
	var started sync.WaitGroup
	started.Add(2)

	bothStarted := make(chan struct{})
	go func() {
		started.Wait()
		close(bothStarted)
	}()

	// Each object handler blocks until the other has also been entered, so a
	// sequential implementation cannot get past the first.
	onObject := func() {
		started.Done()
		select {
		case <-bothStarted:
		case <-time.After(5 * time.Second):
			t.Error("second artefact fetch did not start while the first was in flight")
		}
	}

	client := newTestClient(t, artefactStub(t, map[string]string{
		testPrefix + "model.txt":    "tree\n",
		testPrefix + "serving.json": `{}`,
	}, onObject))

	if _, err := client.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// A promoted version can predate save_serving_artefacts, in which case the
// registry resolves fine and the objects are simply absent.
func TestLoadMissingArtefact(t *testing.T) {
	client := newTestClient(t, artefactStub(t, map[string]string{
		testPrefix + "serving.json": `{}`,
	}, nil))

	_, err := client.Load(context.Background())
	if err == nil {
		t.Fatal("Load succeeded without model.txt, want error")
	}
	if errors.Is(err, ErrNoProductionModel) {
		t.Errorf("error = %v, want a hard failure: the model resolved, its artefacts did not", err)
	}
	// The version and the missing name are what turn this into an actionable
	// report rather than "startup failed".
	for _, want := range []string{"model.txt", "404", "version 3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

// The joined name is one path segment to the API, so its separators have to
// arrive as %2F. Unescaped, this addresses a different endpoint rather than a
// missing object.
func TestObjectURLJoinsAndEscapes(t *testing.T) {
	from := artefactLocation{bucket: testBucket, prefix: strings.TrimSuffix(testPrefix, "/")}

	got := from.objectURL("https://storage.googleapis.com/storage/v1", "model.txt")
	want := "https://storage.googleapis.com/storage/v1/b/" + testBucket +
		"/o/models%2F20260809T233603Z%2Fmodel.txt?alt=media"
	if got != want {
		t.Errorf("objectURL() = %q, want %q", got, want)
	}
}

func TestParseGCSURI(t *testing.T) {
	tests := []struct {
		name    string
		uri     string
		want    artefactLocation
		wantErr bool
	}{
		{
			name: "bucket and prefix",
			uri:  "gs://b/models/ts",
			want: artefactLocation{bucket: "b", prefix: "models/ts"},
		},
		{
			name: "trailing slash",
			uri:  "gs://b/models/ts/",
			want: artefactLocation{bucket: "b", prefix: "models/ts"},
		},
		{name: "bucket only", uri: "gs://b", want: artefactLocation{bucket: "b"}},
		{name: "not a gs uri", uri: "https://example.invalid/b", wantErr: true},
		{name: "no bucket", uri: "gs://", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseGCSURI(tt.uri)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseGCSURI(%q) = %+v, want error", tt.uri, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGCSURI(%q): %v", tt.uri, err)
			}
			if got != tt.want {
				t.Errorf("parseGCSURI(%q) = %+v, want %+v", tt.uri, got, tt.want)
			}
		})
	}
}
