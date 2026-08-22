package registry

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"
	"sync"

	"github.com/AndyHolt/will-it-rain/backend-go/internal/httpx"
)

// The two files save_serving_artefacts writes into the registry prefix
// (pipeline/src/pipeline/components/train.py). This is the whole Go serving
// contract: the LightGBM booster in its native text format, and the metadata
// needed to assemble a feature vector and calibrate a score.
const (
	modelObject   = "model.txt"
	servingObject = "serving.json"
)

// Champion is the @production version together with the artefacts needed to
// serve it.
type Champion struct {
	ProductionModel

	// ModelText is the LightGBM native model text, as parsed by leaves.
	ModelText []byte

	// ServingJSON is the serving contract: feature_cols, lag_hours,
	// sparse_columns, threshold, prediction_window_hours, isotonic knots.
	ServingJSON []byte
}

// Load resolves @production and fetches its serving artefacts.
//
// Returns an error wrapping ErrNoProductionModel when nothing is promoted,
// which callers should treat as "start anyway and serve 503" rather than as a
// failure — see the Python backend's lifespan for the behaviour being matched.
func (c *Client) Load(ctx context.Context) (Champion, error) {
	production, err := c.ResolveProduction(ctx)
	if err != nil {
		return Champion{}, err
	}

	from, err := parseGCSURI(production.ArtifactURI)
	if err != nil {
		return Champion{}, fmt.Errorf("version %s: %w", production.VersionID, err)
	}

	artefacts, err := c.fetchObjects(ctx, from, modelObject, servingObject)
	if err != nil {
		return Champion{}, fmt.Errorf(
			"fetching serving artefacts for version %s from %s: %w",
			production.VersionID, production.ArtifactURI, err,
		)
	}

	return Champion{
		ProductionModel: production,
		ModelText:       artefacts[modelObject],
		ServingJSON:     artefacts[servingObject],
	}, nil
}

// fetchObjects downloads names from under a prefix concurrently, keyed by name.
func (c *Client) fetchObjects(
	ctx context.Context, from artefactLocation, names ...string,
) (map[string][]byte, error) {
	// Cancel the siblings as soon as one fails: on a version that predates
	// the serving contract both 404, and there is no point waiting out the
	// second once the first has answered.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		bodies  = make(map[string][]byte, len(names))
		failure error
	)

	for _, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()

			body, err := httpx.Get(ctx, c.http, from.objectURL(c.storageBaseURL, name))

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				// Keep the first failure: later ones are usually just the
				// cancellation this one triggered.
				if failure == nil {
					failure = fmt.Errorf("%s: %w", name, err)
					cancel()
				}
				return
			}
			bodies[name] = body
		}()
	}
	wg.Wait()

	if failure != nil {
		return nil, failure
	}
	return bodies, nil
}

// artefactLocation is where a version's artefacts live: the bucket the registry
// named, and the prefix within it that register.py uploaded them to.
type artefactLocation struct {
	bucket string
	prefix string
}

// objectURL builds a GCS JSON API media download URL for one artefact.
//
// Joining and escaping belong together: the joined name is a single path
// segment as far as the API is concerned, so its slashes have to survive as
// %2F. Escaping the parts separately, or not at all, addresses a different
// endpoint rather than a missing object.
func (p artefactLocation) objectURL(baseURL, name string) string {
	return fmt.Sprintf(
		"%s/b/%s/o/%s?alt=media",
		baseURL, url.PathEscape(p.bucket), url.PathEscape(path.Join(p.prefix, name)),
	)
}

// parseGCSURI splits a gs://bucket/prefix URI. The bucket is never configured:
// it arrives on the artifactUri the registry returns, so the service follows a
// promotion that moves buckets without redeployment.
func parseGCSURI(uri string) (artefactLocation, error) {
	rest, ok := strings.CutPrefix(uri, "gs://")
	if !ok {
		return artefactLocation{}, fmt.Errorf("artifactUri %q is not a gs:// URI", uri)
	}
	bucket, prefix, _ := strings.Cut(rest, "/")
	if bucket == "" {
		return artefactLocation{}, fmt.Errorf("artifactUri %q names no bucket", uri)
	}
	return artefactLocation{bucket: bucket, prefix: strings.Trim(prefix, "/")}, nil
}
