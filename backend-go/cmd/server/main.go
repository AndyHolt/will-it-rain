// Command server runs the will-it-rain prediction API.
//
// It resolves the model aliased @production in the Vertex Model Registry once
// at startup, then answers /api/predict from the live Open-Meteo forecast. A
// newly promoted model is picked up by rolling the service, not by reloading
// in place — see model_refresher/.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/AndyHolt/will-it-rain/backend-go/internal/forecast"
	"github.com/AndyHolt/will-it-rain/backend-go/internal/registry"
	"github.com/AndyHolt/will-it-rain/backend-go/internal/server"
)

const (
	// startupTimeout bounds the whole model load: two Vertex round trips and
	// two GCS fetches, each already retried inside internal/httpx. Cloud Run
	// allows four minutes to start listening, so failing well inside that
	// turns a wedged dependency into a fast crash-and-retry rather than a slow
	// one.
	startupTimeout = 60 * time.Second

	// shutdownTimeout is how long in-flight requests get after SIGTERM. Cloud
	// Run follows with SIGKILL after 10s, and a prediction takes well under a
	// second.
	shutdownTimeout = 8 * time.Second

	// readHeaderTimeout bounds a client that opens a connection and then
	// dawdles over the request line. Only worth setting because the service is
	// publicly reachable.
	readHeaderTimeout = 10 * time.Second
)

func main() {
	logger := newLogger()
	if err := run(context.Background(), logger); err != nil {
		logger.Error("server exited", "error", err)
		os.Exit(1)
	}
}

// run is main with an error return, so everything that can fail before the
// listener opens reports through one path.
func run(ctx context.Context, logger *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	// Cloud Run signals a scale-down or a replacement revision with SIGTERM.
	// Taking it on the context that startup runs under means an instance torn
	// down mid-load stops loading rather than finishing work for a service
	// that is going away.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	forecasts, err := forecast.New(forecast.Config{
		Latitude:  cfg.latitude,
		Longitude: cfg.longitude,
	})
	if err != nil {
		return fmt.Errorf("forecast client: %w", err)
	}

	// Load both the model and current forecast concurrently. Cloud Run scales
	// to zero, which makes this a common path for many requests, not a
	// once-per-deploy delay which the user never sees while services rotate.
	startupCtx, abandon := context.WithTimeout(ctx, startupTimeout)
	defer abandon()

	started := time.Now()

	var (
		model    *server.Model
		modelErr error
	)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		model, modelErr = loadModel(startupCtx, cfg, logger)
		if modelErr != nil {
			// An instance with no model is not going to serve, so there is
			// nothing left for a forecast to be warm for. Abandoning it here
			// rather than waiting out its budget is what keeps a doomed
			// startup quick to fail, and so quick for Cloud Run to retry.
			abandon()
		}
	}()

	go func() {
		defer wg.Done()
		warmForecast(startupCtx, forecasts, logger)
	}()

	wg.Wait()

	if modelErr != nil {
		return modelErr
	}
	logger.Info("startup complete", "elapsed_ms", time.Since(started).Milliseconds())

	return serve(ctx, cfg.port, server.New(model, forecasts, logger).Handler(), logger)
}

// warmForecast fills the forecast cache so the first request does not have to.
//
// Best-effort, if forecast unavailable, just leave cache empty to be fetched when handling request.
func warmForecast(ctx context.Context, forecasts server.Fetcher, logger *slog.Logger) {
	started := time.Now()
	if _, err := forecasts.Fetch(ctx); err != nil {
		// A cancelled fetch is startup being abandoned — a failed model load,
		// or a SIGTERM — and both of those report themselves. A deadline or a
		// refusal from Open-Meteo is this fetch's own news, and is worth
		// saying so the first slow request is attributable.
		if !errors.Is(ctx.Err(), context.Canceled) {
			logger.Warn("could not warm the forecast cache: the first prediction will fetch it",
				"error", err, "elapsed_ms", time.Since(started).Milliseconds())
		}
		return
	}
	logger.Info("warmed the forecast cache", "elapsed_ms", time.Since(started).Milliseconds())
}

// loadModel resolves @production and unpacks its artefacts.
//
// Nothing promoted is not a startup failure: the service comes up, /api/health
// reports no version and /api/predict answers 503. That is the Python
// backend's behaviour, and it is the state the project is in until a pipeline
// run has promoted something.
func loadModel(ctx context.Context, cfg config, logger *slog.Logger) (*server.Model, error) {
	started := time.Now()

	client, err := registry.New(ctx, registry.Config{
		Region:           cfg.region,
		ModelDisplayName: cfg.modelDisplayName,
	})
	if err != nil {
		return nil, fmt.Errorf("model registry: %w", err)
	}

	champion, err := client.Load(ctx)
	if errors.Is(err, registry.ErrNoProductionModel) {
		logger.Warn("no @production model: /api/predict will answer 503 until one is promoted",
			"error", err)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading the @production model: %w", err)
	}

	model, err := server.NewModel(champion, time.Now())
	if err != nil {
		return nil, fmt.Errorf("unpacking model version %s: %w", champion.VersionID, err)
	}

	logger.Info("loaded model",
		"model_version", model.VersionID,
		"model_resource", model.ResourceName,
		"elapsed_ms", time.Since(started).Milliseconds(),
	)
	return model, nil
}

// serve listens until the context ends, then drains.
func serve(ctx context.Context, port string, handler http.Handler, logger *slog.Logger) error {
	srv := &http.Server{
		Addr:              net.JoinHostPort("", port),
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	listening := make(chan error, 1)
	go func() { listening <- srv.ListenAndServe() }()
	logger.Info("listening", "port", port)

	select {
	case err := <-listening:
		// Only reached on a failure to listen or serve: the shutdown below is
		// what closes the server on the normal path.
		return fmt.Errorf("serving: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	// Deliberately not derived from ctx, which is already cancelled — the
	// point of this one is to give in-flight requests a moment after that.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	return nil
}
