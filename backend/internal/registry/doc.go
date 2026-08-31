// Package registry supplies the model this service serves.
//
// It authenticates with Application Default Credentials, resolves the version
// aliased @production in the Vertex Model Registry, and fetches that version's
// serving artefacts — model.txt and serving.json — from the GCS prefix the
// registry points at. Client.Load does all three and returns a Champion.
package registry
