package forecast

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Random point. The awkward precision is the point: it survives the round trip
// through the query string only if the coordinate is not rounded.
const (
	testLatitude  = 12.3456789
	testLongitude = -65.4321
)

// newTestClient points a Client at a stub Open-Meteo endpoint.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := New(Config{Latitude: testLatitude, Longitude: testLongitude})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client.http = server.Client()
	client.baseURL = server.URL
	return client
}

func TestNewAppliesTheDefaultWindow(t *testing.T) {
	client, err := New(Config{Latitude: testLatitude, Longitude: testLongitude})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// PastHours has to cover the largest lag in serving.json; defaulting it to
	// zero would leave every lagged feature NaN without erroring.
	if client.cfg.PastHours != defaultPastHours {
		t.Errorf("PastHours = %d, want %d", client.cfg.PastHours, defaultPastHours)
	}
	if client.cfg.ForecastHours != defaultForecastHours {
		t.Errorf("ForecastHours = %d, want %d", client.cfg.ForecastHours, defaultForecastHours)
	}
}

func TestNewRejectsUnusableConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		// 0,0 is deliberately absent: it is a real point, so an unset
		// coordinate is indistinguishable from a set one and cannot be
		// rejected here. Only values that are not coordinates at all are.
		{name: "latitude off the globe", cfg: Config{Latitude: 91}},
		{name: "longitude off the globe", cfg: Config{Longitude: -181}},
		{name: "negative past hours", cfg: Config{PastHours: -1}},
		{name: "negative forecast hours", cfg: Config{ForecastHours: -1}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.cfg); err == nil {
				t.Fatalf("New succeeded with %s, want error", test.name)
			}
		})
	}
}

// The request has to ask for what the training-time fetcher asked for: the
// same nine variables, both models, and the FlatBuffers encoding. Any drift
// here feeds the model a frame it was not fitted on.
func TestRequestURLMatchesTheTrainingTimeRequest(t *testing.T) {
	client, err := New(Config{Latitude: testLatitude, Longitude: testLongitude})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	parsed, err := url.Parse(client.requestURL())
	if err != nil {
		t.Fatalf("parsing request URL: %v", err)
	}
	query := parsed.Query()

	want := map[string]string{
		"latitude":       "12.3456789",
		"longitude":      "-65.4321",
		"past_hours":     "24",
		"forecast_hours": "24",
		// Comma-joined rather than repeated, as golden_fixtures/capture.py
		// sent them when it recorded testdata/forecast.fb.
		"hourly": "temperature_2m,relative_humidity_2m,apparent_temperature," +
			"precipitation,rain,showers,cloud_cover,wind_speed_10m,wind_direction_10m",
		"models": "ukmo_uk_deterministic_2km,ecmwf_ifs",
		// JSON would quantise: integer humidity, 1dp temperature.
		"format": "flatbuffers",
	}
	for key, want := range want {
		if got := query.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// The API rejects an exponent, which is what %v would produce for a small
// coordinate — a location near the Greenwich meridian, say.
func TestFormatCoordinateAvoidsExponentNotation(t *testing.T) {
	if got := formatCoordinate(0.0000001); strings.ContainsAny(got, "eE") {
		t.Errorf("formatCoordinate(1e-7) = %q, want plain decimal notation", got)
	}
}

func TestFetchReturnsTheDecodedForecast(t *testing.T) {
	fixture := readTestdata(t, "forecast.fb")
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// The stub answers anything, so assert here that Fetch asked for the
		// configured location rather than the package defaults.
		if got := r.URL.Query().Get("latitude"); got != "12.3456789" {
			t.Errorf("requested latitude = %q, want the configured one", got)
		}
		if _, err := w.Write(fixture); err != nil {
			t.Errorf("writing stub response: %v", err)
		}
	})

	forecast, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	want := readGolden(t)
	if len(forecast.Times) != len(want.TimesUTC) {
		t.Errorf("fetched %d hours, want %d", len(forecast.Times), len(want.TimesUTC))
	}
	if len(forecast.Columns) != len(want.Columns) {
		t.Errorf("fetched %d columns, want %d", len(forecast.Columns), len(want.Columns))
	}
}

// A forecast that does not arrive and a forecast that does not parse are
// different faults with different fixes, so the wrapping has to tell them
// apart at a glance in the Cloud Run log.
func TestFetchDistinguishesTransportFromDecodeFailures(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{
			name: "upstream failure",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "rate limited", http.StatusTooManyRequests)
			},
			want: "fetching forecast",
		},
		{
			name: "unparseable body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				// What a captive portal or a changed content type looks like.
				if _, err := w.Write([]byte("<html>not flatbuffers</html>")); err != nil {
					t.Errorf("writing stub response: %v", err)
				}
			},
			want: "decoding",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newTestClient(t, test.handler)

			_, err := client.Fetch(context.Background())
			if err == nil {
				t.Fatalf("Fetch succeeded on %s, want error", test.name)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error %q does not mention %q", err, test.want)
			}
		})
	}
}

// The caller's context is the request budget; requestTimeout is only a
// backstop against a hung connection.
func TestFetchHonoursContextCancellation(t *testing.T) {
	client := newTestClient(t, func(_ http.ResponseWriter, _ *http.Request) {})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.Fetch(ctx); err == nil {
		t.Fatal("Fetch succeeded on a cancelled context, want error")
	}
}
