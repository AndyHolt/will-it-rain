package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/AndyHolt/will-it-rain/backend-go/internal/forecast"
)

// frontend surfaces detail as the text of its error message
// (frontend/src/forecast/fetch.ts),
const (
	noModelDetail   = "Model not loaded."
	forecastDetail  = "Forecast unavailable."
	predictDetail   = "Prediction failed."
	notFoundDetail  = "Not Found."
	methodDetail    = "Method Not Allowed."
	encodingDetail  = "Internal Server Error."
	jsonContentType = "application/json"
)

// Fetcher is the forecast source a prediction scores. Narrowed to the one
// method this package calls, so a test needs no HTTP server: *forecast.Client
// is the production implementation, and it caches, so a request costs an
// Open-Meteo round trip only once the cached forecast has aged out.
type Fetcher interface {
	Fetch(ctx context.Context) (*forecast.Forecast, error)
}

// Server answers the API routes from one loaded model.
type Server struct {
	// model is nil when the registry held no @production version at startup.
	// That is a legitimate state — the service still serves /api/health, which
	// is what says why /api/predict is answering 503.
	model    *Model
	forecast Fetcher
	log      *slog.Logger

	// now is the clock the anchor is picked against, a seam for tests.
	now func() time.Time
}

// New returns a Server serving model, which may be nil.
func New(model *Model, fetcher Fetcher, log *slog.Logger) *Server {
	return &Server{model: model, forecast: fetcher, log: log, now: time.Now}
}

// Handler returns the two routes plus the fallback that answers for
// everything else.
func (s *Server) Handler() http.Handler {
	routes := map[string]http.HandlerFunc{
		"/api/health":  s.health,
		"/api/predict": s.predict,
	}

	mux := http.NewServeMux()
	for path, handler := range routes {
		mux.HandleFunc("GET "+path, handler)
	}
	mux.HandleFunc("/", s.unserved(routes))
	return mux
}

// unserved answers the requests the routes above do not, in the same JSON
// shape as the ones they do.
//
// It has to tell a missing path from a wrong method itself. ServeMux answers
// 405 only when nothing matched at all, and a "/" pattern matches everything —
// so registering a fallback for the plain-text 404 silently turns every method
// mismatch into a 404 too.
func (s *Server) unserved(routes map[string]http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, served := routes[r.URL.Path]; served {
			w.Header().Set("Allow", http.MethodGet)
			s.writeError(w, http.StatusMethodNotAllowed, methodDetail)
			return
		}
		s.writeError(w, http.StatusNotFound, notFoundDetail)
	}
}

// HealthResponse is /api/health's body. The three model fields are pointers
// because they are null until something is promoted, which is exactly the
// state a caller checks health to find out about.
type HealthResponse struct {
	Status        string     `json:"status"`
	ModelVersion  *string    `json:"model_version"`
	ModelResource *string    `json:"model_resource"`
	LoadedAtUTC   *time.Time `json:"loaded_at_utc"`
}

// PredictResponse is /api/predict's body. Field names and order mirror the
// Python backend's PredictResponse, which the frontend is typed against.
type PredictResponse struct {
	AnchorUTC      time.Time `json:"anchor_utc"`
	WindowEndUTC   time.Time `json:"window_end_utc"`
	RawProb        float64   `json:"raw_prob"`
	CalibratedProb float64   `json:"calibrated_prob"`
	Threshold      float64   `json:"threshold"`
	WillRain       bool      `json:"will_rain"`
	ModelVersion   string    `json:"model_version"`
}

// errorResponse is FastAPI's error shape, which every non-200 here keeps.
type errorResponse struct {
	Detail string `json:"detail"`
}

// health reports what is loaded, and answers 200 whether or not anything is.
// Reporting "no model" as a failure would leave the one question health exists
// to answer unanswerable.
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	response := HealthResponse{Status: "ok"}
	if s.model != nil {
		response.ModelVersion = &s.model.VersionID
		response.ModelResource = &s.model.ResourceName
		response.LoadedAtUTC = &s.model.LoadedAt
	}
	s.writeJSON(w, http.StatusOK, response)
}

// predict scores the current forecast.
func (s *Server) predict(w http.ResponseWriter, r *http.Request) {
	if s.model == nil {
		// An instance that started before the first promotion is the one state
		// this endpoint cannot serve. 503 and this text are the Python
		// backend's.
		s.writeError(w, http.StatusServiceUnavailable, noModelDetail)
		return
	}

	current, err := s.forecast.Fetch(r.Context())
	if err != nil {
		// The cause is logged rather than returned: it carries the request URL
		// and whatever Open-Meteo said, neither of which a public caller has
		// any use for.
		s.log.Error("fetching the forecast", "error", err)
		s.writeError(w, http.StatusInternalServerError, forecastDetail)
		return
	}

	prediction, err := s.model.Predict(current, s.now())
	if err != nil {
		s.log.Error("predicting", "error", err, "model_version", s.model.VersionID)
		s.writeError(w, http.StatusInternalServerError, predictDetail)
		return
	}

	s.writeJSON(w, http.StatusOK, prediction)
}

func (s *Server) writeError(w http.ResponseWriter, status int, detail string) {
	s.writeJSON(w, status, errorResponse{Detail: detail})
}

// writeJSON sends payload as the whole body.
//
// Marshalled before anything is written: an encoder writing straight to the
// ResponseWriter would have sent 200 and a partial body by the time it
// discovered it could not finish.
func (s *Server) writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		s.log.Error("encoding the response", "error", err)
		body, status = []byte(`{"detail":"`+encodingDetail+`"}`), http.StatusInternalServerError
	}

	w.Header().Set("Content-Type", jsonContentType)
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		// The client went away, or the connection broke mid-write. Nothing to
		// recover — the status line is long gone — so this is a log line and
		// not an error path.
		s.log.Warn("writing the response", "error", err)
	}
}
