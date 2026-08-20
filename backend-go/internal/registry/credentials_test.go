package registry

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveProject(t *testing.T) {
	tests := []struct {
		name       string
		envProject string
		adcProject string
		want       string
		wantErr    bool
	}{
		{name: "env wins over adc", envProject: "from-env", adcProject: "from-adc", want: "from-env"},
		{name: "adc used when env unset", adcProject: "from-adc", want: "from-adc"},
		{name: "env used when adc empty", envProject: "from-env", want: "from-env"},
		{name: "neither is an error", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveProject(tt.envProject, tt.adcProject)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveProject(%q, %q) = %q, want error", tt.envProject, tt.adcProject, got)
				}
				// The message has to name the way out, since an empty
				// project otherwise surfaces as a Vertex 404.
				if !strings.Contains(err.Error(), "PROJECT") {
					t.Errorf("error %q does not mention PROJECT", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveProject(%q, %q): %v", tt.envProject, tt.adcProject, err)
			}
			if got != tt.want {
				t.Errorf("resolveProject(%q, %q) = %q, want %q", tt.envProject, tt.adcProject, got, tt.want)
			}
		})
	}
}

// writeADC points GOOGLE_APPLICATION_CREDENTIALS at a credentials file holding
// contents. Authorized-user credentials are used because they need no private
// key, so nothing here touches the network.
func writeADC(t *testing.T, contents string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "adc.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing ADC file: %v", err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
}

const authorizedUserADC = `{
  "type": "authorized_user",
  "client_id": "test-client-id",
  "client_secret": "test-client-secret",
  "refresh_token": "test-refresh-token"
}`

func TestResolveCredentials(t *testing.T) {
	writeADC(t, authorizedUserADC)
	t.Setenv("PROJECT", "will-it-rain-test")

	creds, err := ResolveCredentials(context.Background())
	if err != nil {
		t.Fatalf("ResolveCredentials: %v", err)
	}
	if creds.ProjectID != "will-it-rain-test" {
		t.Errorf("ProjectID = %q, want %q", creds.ProjectID, "will-it-rain-test")
	}
	if creds.TokenSource == nil {
		t.Error("TokenSource is nil")
	}
}

func TestResolveCredentialsWithoutADC(t *testing.T) {
	writeADC(t, "not json")
	t.Setenv("PROJECT", "will-it-rain-test")

	if _, err := ResolveCredentials(context.Background()); err == nil {
		t.Fatal("ResolveCredentials succeeded with unreadable credentials, want error")
	} else if !strings.Contains(err.Error(), "Application Default Credentials") {
		t.Errorf("error %q does not name Application Default Credentials", err)
	}
}
