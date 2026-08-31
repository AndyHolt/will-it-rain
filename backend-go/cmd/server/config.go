package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

// defaultPort is what the service listens on when Cloud Run injects no PORT,
// and the container port cloud_run.tf declares.
const defaultPort = "8080"

// config is the service's deployment-time environment.
//
// The GCP project is deliberately absent: internal/registry resolves it from
// Application Default Credentials, so the binary has no project compiled into
// it and runs unmodified in any of them. The region cannot be resolved that
// way — ADC carries a project and never a region — so REGION stays injected,
// which cloud_run.tf already does.
type config struct {
	latitude  float64
	longitude float64

	region           string
	modelDisplayName string
	port             string
}

// loadConfig reads the environment, failing on anything missing rather than
// starting with a default nobody chose. A wrong location is a service serving
// predictions for somewhere else, which no response distinguishes from a right
// one.
func loadConfig() (config, error) {
	cfg := config{
		region:           os.Getenv("REGION"),
		modelDisplayName: os.Getenv("MODEL_DISPLAY_NAME"),
		port:             os.Getenv("PORT"),
	}

	var err error
	if cfg.latitude, err = coordinateEnv("LATITUDE"); err != nil {
		return config{}, err
	}
	if cfg.longitude, err = coordinateEnv("LONGITUDE"); err != nil {
		return config{}, err
	}
	if cfg.region == "" {
		return config{}, errors.New("REGION is not set: it is the Vertex region, e.g. europe-west2")
	}
	if cfg.port == "" {
		cfg.port = defaultPort
	}

	// MODEL_DISPLAY_NAME is left empty when unset: internal/registry defaults
	// it, and defaulting it twice is how the two come to disagree.
	return cfg, nil
}

// coordinateEnv reads a required coordinate. Absent and unparseable get their
// own messages — one is a deployment variable nobody set, the other a typo in
// one that was.
func coordinateEnv(name string) (float64, error) {
	raw, ok := os.LookupEnv(name)
	if !ok || raw == "" {
		return 0, fmt.Errorf("%s is not set", name)
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a number", name, raw)
	}
	return value, nil
}
