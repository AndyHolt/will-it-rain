package main

import (
	"strings"
	"testing"
)

// setEnv sets the service's whole environment for one test, including the
// variables a case wants absent — the process may be run anywhere, and a
// LATITUDE inherited from the developer's shell would make a
// "missing coordinate" case pass for the wrong reason.
func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, name := range []string{"LATITUDE", "LONGITUDE", "LOCATION", "MODEL_DISPLAY_NAME", "PORT"} {
		t.Setenv(name, env[name])
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"LATITUDE":  "55.9533",
		"LONGITUDE": "-3.1883",
		"LOCATION":  "europe-west2",
	}
}

// TestLoadConfigReadsTheDeploymentEnvironment covers what cloud_run.tf injects,
// plus the PORT Cloud Run adds and the display name it may leave to the
// registry's default.
func TestLoadConfigReadsTheDeploymentEnvironment(t *testing.T) {
	env := validEnv()
	env["MODEL_DISPLAY_NAME"] = "will-it-rain"
	env["PORT"] = "9090"
	setEnv(t, env)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.latitude != 55.9533 || cfg.longitude != -3.1883 {
		t.Errorf("coordinates = %v, %v, want 55.9533, -3.1883", cfg.latitude, cfg.longitude)
	}
	if cfg.region != "europe-west2" {
		t.Errorf("region = %q, want europe-west2", cfg.region)
	}
	if cfg.modelDisplayName != "will-it-rain" {
		t.Errorf("modelDisplayName = %q, want will-it-rain", cfg.modelDisplayName)
	}
	if cfg.port != "9090" {
		t.Errorf("port = %q, want 9090", cfg.port)
	}
}

// TestLoadConfigDefaultsThePort covers running without Cloud Run's PORT — and
// the display name staying empty, which is how internal/registry gets to apply
// its own default rather than having a second one applied here.
func TestLoadConfigDefaultsThePort(t *testing.T) {
	setEnv(t, validEnv())

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.port != defaultPort {
		t.Errorf("port = %q, want %q", cfg.port, defaultPort)
	}
	if cfg.modelDisplayName != "" {
		t.Errorf("modelDisplayName = %q, want it left to the registry default", cfg.modelDisplayName)
	}
}

// TestLoadConfigRequiresItsDeploymentVariables pins that a missing one stops
// startup. None of them has a defensible default: a made-up coordinate serves
// predictions for somewhere else, and a made-up region resolves a model in a
// project that may not have one.
func TestLoadConfigRequiresItsDeploymentVariables(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"no latitude", map[string]string{"LONGITUDE": "-3.1883", "LOCATION": "europe-west2"}, "LATITUDE is not set"},
		{"no longitude", map[string]string{"LATITUDE": "55.9533", "LOCATION": "europe-west2"}, "LONGITUDE is not set"},
		{"no location", map[string]string{"LATITUDE": "55.9533", "LONGITUDE": "-3.1883"}, "LOCATION is not set"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setEnv(t, tc.env)

			_, err := loadConfig()
			if err == nil {
				t.Fatal("loadConfig accepted an incomplete environment")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestLoadConfigRejectsACoordinateThatIsNotANumber is a separate message from
// the missing case on purpose: one is a variable nobody set, the other a typo
// in one that was, and they are fixed in different places.
func TestLoadConfigRejectsACoordinateThatIsNotANumber(t *testing.T) {
	env := validEnv()
	env["LATITUDE"] = "55.9533N"
	setEnv(t, env)

	_, err := loadConfig()
	if err == nil {
		t.Fatal("loadConfig accepted LATITUDE=55.9533N")
	}
	if !strings.Contains(err.Error(), `LATITUDE="55.9533N"`) {
		t.Errorf("error %q does not quote the value it rejected", err)
	}
}
