package forecast

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AndyHolt/will-it-rain/backend-go/internal/httpx"
)

const currentForecastBaseURL = "https://api.open-meteo.com/v1/forecast"

// Must be kept in sync with training features!
var (
	forecastVariables = []string{
		"temperature_2m",
		"relative_humidity_2m",
		"apparent_temperature",
		"precipitation",
		"rain",
		"showers",
		"cloud_cover",
		"wind_speed_10m",
		"wind_direction_10m",
	}

	forecastModels = []string{
		"ukmo_uk_deterministic_2km",
		"ecmwf_ifs",
	}
)

const (
	// PastHours has to cover the largest lag in serving.json; 24 is comfortably
	// above the configured lags.
	defaultPastHours     = 24
	defaultForecastHours = 24

	// Backstop only — callers pass a context whose deadline is the real
	// budget. This window is a few KB and answers in well under a second; the
	// timeout is here so a hung connection fails the request rather than
	// wedging startup.
	requestTimeout = 15 * time.Second
)

// Config is the location to forecast for, plus the window around it.
type Config struct {
	// Latitude and Longitude come from environment variables.
	Latitude  float64
	Longitude float64

	// PastHours and ForecastHours default to 24 each.
	PastHours     int
	ForecastHours int
}

func (c *Config) applyDefaults() error {
	// No required-field check on the coordinates: 0,0 is a legal point in the
	// Gulf of Guinea, so an unset float is indistinguishable from a set one.
	// Requiring LATITUDE and LONGITUDE is the caller's job; this only rejects
	// values that are not coordinates at all.
	if c.Latitude < -90 || c.Latitude > 90 {
		return fmt.Errorf("forecast.Config.Latitude %v is not a latitude", c.Latitude)
	}
	if c.Longitude < -180 || c.Longitude > 180 {
		return fmt.Errorf("forecast.Config.Longitude %v is not a longitude", c.Longitude)
	}
	if c.PastHours == 0 {
		c.PastHours = defaultPastHours
	}
	if c.ForecastHours == 0 {
		c.ForecastHours = defaultForecastHours
	}
	if c.PastHours < 0 || c.ForecastHours < 0 {
		return fmt.Errorf(
			"forecast.Config hours must be positive: past=%d forecast=%d",
			c.PastHours, c.ForecastHours,
		)
	}
	return nil
}

// Forecast is one fetch's worth of hourly data in the canonical column shape.
// TODO consider whether a row-based data structure would be preferable here.
// This would avoid Times and Columns[feature_name] ending up with different
// lengths, and ensure full population of columns.
type Forecast struct {
	// Times are the hourly UTC timestamps, ascending, covering every hour any
	// model reported.
	Times []time.Time

	// Columns is keyed "{model}__{variable}". Every slice is len(Times) long,
	// NaN at hours the model did not report — LightGBM handles missing
	// values natively, so a ragged model is not an error.
	Columns map[string][]float64
}

// Client fetches the forecast for one fixed location.
type Client struct {
	http *http.Client
	cfg  Config

	// Overridden in tests.
	baseURL string
}

// New validates cfg and returns a Client addressing the configured location.
func New(cfg Config) (*Client, error) {
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}
	return &Client{
		http:    &http.Client{Timeout: requestTimeout},
		cfg:     cfg,
		baseURL: currentForecastBaseURL,
	}, nil
}

// Fetch returns the current forecast for the configured location.
func (c *Client) Fetch(ctx context.Context) (*Forecast, error) {
	payload, err := httpx.Get(ctx, c.http, c.requestURL())
	if err != nil {
		return nil, fmt.Errorf("fetching forecast from Open-Meteo: %w", err)
	}
	forecast, err := decode(payload)
	if err != nil {
		return nil, fmt.Errorf("decoding Open-Meteo forecast: %w", err)
	}
	return forecast, nil
}

// requestURL builds the forecast request. hourly and models are comma-joined
// rather than repeated, matching what golden_fixtures/capture.py sent when it
// recorded testdata/forecast.fb.
func (c *Client) requestURL() string {
	query := url.Values{
		"latitude":       {formatCoordinate(c.cfg.Latitude)},
		"longitude":      {formatCoordinate(c.cfg.Longitude)},
		"past_hours":     {strconv.Itoa(c.cfg.PastHours)},
		"forecast_hours": {strconv.Itoa(c.cfg.ForecastHours)},
		"hourly":         {strings.Join(forecastVariables, ",")},
		"models":         {strings.Join(forecastModels, ",")},
		"format":         {"flatbuffers"},
	}
	return c.baseURL + "?" + query.Encode()
}

// formatCoordinate renders a coordinate at full precision and without an
// exponent, which the API rejects.
func formatCoordinate(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
