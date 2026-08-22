package registry

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Status and body handling is internal/httpx's, and tested there. What is
// registry's own is the decode.
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
