package registry

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// The transport is shared by the Vertex and GCS calls, so what a failure
// reports is decided here rather than separately at either call site.
func TestGetBytesReportsStatusAndBody(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"caller lacks storage.objects.get"}}`, http.StatusForbidden)
	})

	_, err := client.getBytes(context.Background(), client.storageBaseURL)
	if err == nil {
		t.Fatal("getBytes succeeded against a 403, want error")
	}

	var status *statusError
	if !errors.As(err, &status) {
		t.Fatalf("error %v is not a *statusError", err)
	}
	if status.code != http.StatusForbidden {
		t.Errorf("code = %d, want %d", status.code, http.StatusForbidden)
	}
	// Without the body, a permissions failure and a wrong URL look alike.
	if !strings.Contains(status.body, "storage.objects.get") {
		t.Errorf("body = %q, does not carry the server's explanation", status.body)
	}
}

func TestGetBytesReturnsBodyVerbatim(t *testing.T) {
	// Artefact bodies are not JSON — model.txt is LightGBM's own text format,
	// and it has to arrive byte-for-byte for leaves to parse it.
	const body = "tree\nversion=v4\nnum_class=1\n"

	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("writing stub response: %v", err)
		}
	})

	got, err := client.getBytes(context.Background(), client.storageBaseURL)
	if err != nil {
		t.Fatalf("getBytes: %v", err)
	}
	if string(got) != body {
		t.Errorf("getBytes() = %q, want %q", got, body)
	}
}

func TestGetJSONRejectsUndecodableBody(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte("<html>proxy says no</html>")); err != nil {
			t.Errorf("writing stub response: %v", err)
		}
	})

	var out struct{}
	err := client.getJSON(context.Background(), client.vertexBaseURL, &out)
	if err == nil {
		t.Fatal("getJSON succeeded on a non-JSON 200, want error")
	}
	// A 200 carrying HTML means something answered that was not the API;
	// "decoding" is what distinguishes it from a transport failure.
	if !strings.Contains(err.Error(), "decoding") {
		t.Errorf("error %q does not say what failed", err)
	}
}
