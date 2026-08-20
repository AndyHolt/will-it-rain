package registry

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// cloudPlatformScope covers both the Vertex Model Registry reads and the GCS
// object reads that follow them, so one credential serves the whole load.
const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// Credentials carries what Application Default Credentials yield: a token
// source for the API calls, and the GCP project those calls address.
type Credentials struct {
	TokenSource oauth2.TokenSource
	ProjectID   string
}

// ResolveCredentials returns ADC and the project to address them at.
//
// The project is resolved PROJECT env var → the project ADC carries → hard
// failure. Terraform injects nothing here: on Cloud Run the credentials come
// from the metadata server and carry the project the service runs in, so the
// binary has no project compiled into it and runs unmodified anywhere. PROJECT
// stays an override for the cases ADC cannot answer for itself.
//
// Failing loudly on an empty project is deliberate — user credentials with no
// quota project set yield "", which would otherwise reach Vertex as a
// malformed URL and come back as a 404 that reads like a missing model.
func ResolveCredentials(ctx context.Context) (*Credentials, error) {
	creds, err := google.FindDefaultCredentials(ctx, cloudPlatformScope)
	if err != nil {
		return nil, fmt.Errorf(
			"no Application Default Credentials: run `gcloud auth application-default login`: %w",
			err,
		)
	}

	project, err := resolveProject(os.Getenv("PROJECT"), creds.ProjectID)
	if err != nil {
		return nil, err
	}

	return &Credentials{TokenSource: creds.TokenSource, ProjectID: project}, nil
}

// resolveProject applies the PROJECT → ADC → failure chain. Split out from
// ResolveCredentials so the precedence behaviour is pinned by testing without
// credentials.
func resolveProject(envProject, adcProject string) (string, error) {
	if envProject != "" {
		return envProject, nil
	}
	if adcProject != "" {
		return adcProject, nil
	}
	return "", errors.New(
		"no project in the Application Default Credentials: set PROJECT explicitly, " +
			"or re-authenticate against a project",
	)
}
