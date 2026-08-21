package forecast

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/AndyHolt/will-it-rain/backend-go/internal/openmeteo"
)

// readTestdata reads one of the checked-in golden fixtures, which live at the
// module root so both this package and internal/features can share them.
func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return body
}

// goldenForecast is the "forecast" section of testdata/expected.json: what the
// Python fetcher made of the same bytes as testdata/forecast.fb. Values are
// *float64 so that a JSON null — a missing value, NaN on both sides — stays
// distinguishable from a reported zero, which for rain is the common case.
type goldenForecast struct {
	TimesUTC []string              `json:"times_utc"`
	Columns  map[string][]*float64 `json:"columns"`
}

func readGolden(t *testing.T) goldenForecast {
	t.Helper()
	var expected struct {
		Forecast goldenForecast `json:"forecast"`
	}
	if err := json.Unmarshal(readTestdata(t, "expected.json"), &expected); err != nil {
		t.Fatalf("decoding expected.json: %v", err)
	}
	return expected.Forecast
}

// This is the parity assertion the whole package exists to satisfy: the same
// payload has to become the same column map here as it does through
// shared/forecast.py, exactly, or the model is served a different frame from
// the one it was fitted on.
func TestDecodeMatchesThePythonColumnMap(t *testing.T) {
	got, err := decode(readTestdata(t, "forecast.fb"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := readGolden(t)

	if len(got.Times) != len(want.TimesUTC) {
		t.Fatalf("decoded %d hours, want %d", len(got.Times), len(want.TimesUTC))
	}
	for i, stamp := range want.TimesUTC {
		if formatted := got.Times[i].Format(time.RFC3339); formatted != stamp {
			t.Errorf("Times[%d] = %s, want %s", i, formatted, stamp)
		}
	}

	if len(got.Columns) != len(want.Columns) {
		t.Errorf("decoded %d columns, want %d", len(got.Columns), len(want.Columns))
	}
	for name, wantValues := range want.Columns {
		gotValues, ok := got.Columns[name]
		if !ok {
			t.Errorf("column %s missing", name)
			continue
		}
		if len(gotValues) != len(wantValues) {
			t.Errorf("column %s has %d values, want %d", name, len(gotValues), len(wantValues))
			continue
		}
		for i, want := range wantValues {
			// Exact, not approximate. Both sides widen the same float32, so
			// any difference at all is a decode bug rather than rounding.
			if !sameValue(gotValues[i], want) {
				t.Errorf("column %s[%d] = %v, want %v", name, i, gotValues[i], describe(want))
			}
		}
	}
}

// sameValue compares a decoded value against the golden one, where a JSON null
// stands for the NaN Python held.
func sameValue(got float64, want *float64) bool {
	if want == nil {
		return math.IsNaN(got)
	}
	return got == *want
}

func describe(want *float64) string {
	if want == nil {
		return "NaN"
	}
	return strconv.FormatFloat(*want, 'g', -1, 64)
}

// The fixture is a live two-model response, so it already proves the framing;
// this pins it on data whose values say which message they came from, so that
// stopping at the first root or mixing the two up cannot pass.
func TestDecodeReadsEveryMessageInThePayload(t *testing.T) {
	first := newStub(openmeteo.Modelukmo_uk_deterministic_2km)
	second := newStub(openmeteo.Modelecmwf_ifs)
	second.base = 1000

	got, err := decode(append(first.build(t), second.build(t)...))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(got.Columns) != 2*len(forecastVariables) {
		t.Fatalf("decoded %d columns, want %d", len(got.Columns), 2*len(forecastVariables))
	}
	for i, variable := range forecastVariables {
		assertColumn(t, got, "ukmo_uk_deterministic_2km__"+variable, first.value(i, 0))
		assertColumn(t, got, "ecmwf_ifs__"+variable, second.value(i, 0))
	}
}

func assertColumn(t *testing.T, forecast *Forecast, name string, wantFirst float32) {
	t.Helper()
	values, ok := forecast.Columns[name]
	if !ok {
		t.Errorf("column %s missing", name)
		return
	}
	if values[0] != float64(wantFirst) {
		t.Errorf("column %s[0] = %v, want %v", name, values[0], wantFirst)
	}
}

func TestDecodeRejectsMalformedPayloads(t *testing.T) {
	fixture := readTestdata(t, "forecast.fb")

	tests := []struct {
		name    string
		payload []byte
		want    string
	}{
		{
			name:    "empty",
			payload: nil,
			want:    "no responses",
		},
		{
			// A response cut short in transit: the last prefix promises more
			// bytes than arrived. Parsing it anyway reads past the buffer.
			name:    "truncated message",
			payload: fixture[:len(fixture)-16],
			want:    "overruns",
		},
		{
			// Fewer than four trailing bytes cannot even be a size prefix.
			name:    "truncated size prefix",
			payload: append(fixture[:len(fixture):len(fixture)], 0x00, 0x00),
			want:    "truncated size prefix",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decode(test.payload)
			if err == nil {
				t.Fatalf("decode succeeded on a %s payload, want error", test.name)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error %q does not mention %q", err, test.want)
			}
		})
	}
}

// Everything a live response cannot be: the API always answers with the models
// and variables that were asked for, so these paths are only reachable if the
// contract changes underneath the service — which is exactly when a clear
// error matters.
func TestDecodeRejectsResponsesBreakingTheRequestContract(t *testing.T) {
	tests := []struct {
		name string
		stub func(*stubResponse)
		want string
	}{
		{
			name: "unknown model code",
			// Not in EnumNamesModel: the bindings were regenerated behind us,
			// or the response is not the one requested.
			stub: func(s *stubResponse) { s.model = openmeteo.Model(255) },
			want: "unknown model code",
		},
		{
			name: "no hourly block",
			stub: func(s *stubResponse) { s.noHourly = true },
			want: "no hourly data",
		},
		{
			// Variables are read positionally, so a short vector would
			// otherwise silently shift every column by one.
			name: "fewer variables than requested",
			stub: func(s *stubResponse) { s.variables = len(forecastVariables) - 1 },
			want: "variables",
		},
		{
			name: "values shorter than the time index",
			stub: func(s *stubResponse) { s.values = stubHours - 1 },
			want: "values for",
		},
		{
			name: "non-positive interval",
			stub: func(s *stubResponse) { s.interval = 0 },
			want: "interval",
		},
		{
			name: "time range running backwards",
			stub: func(s *stubResponse) { s.end = s.start - 3600 },
			want: "before it starts",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := newStub(openmeteo.Modelecmwf_ifs)
			test.stub(&stub)

			_, err := decode(stub.build(t))
			if err == nil {
				t.Fatalf("decode succeeded on %s, want error", test.name)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error %q does not mention %q", err, test.want)
			}
		})
	}
}

// The outer join is the one piece of decode with no counterpart in a live
// response: both models publish the same 48 hours today, so the ragged case
// can only be built by hand.
func TestMergeFillsHoursAModelDidNotReport(t *testing.T) {
	hour := func(n int64) time.Time { return time.Unix(n*3600, 0).UTC() }

	got, err := merge([]modelForecast{
		{
			name:    "early",
			times:   []time.Time{hour(1), hour(2)},
			columns: map[string][]float64{"early__rain": {1, 2}},
		},
		{
			name:    "late",
			times:   []time.Time{hour(2), hour(3)},
			columns: map[string][]float64{"late__rain": {20, 30}},
		},
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	// The union of both ranges, ascending — not the first model's index, and
	// not map iteration order.
	wantTimes := []time.Time{hour(1), hour(2), hour(3)}
	if len(got.Times) != len(wantTimes) {
		t.Fatalf("merged to %d hours, want %d", len(got.Times), len(wantTimes))
	}
	for i, want := range wantTimes {
		if !got.Times[i].Equal(want) {
			t.Errorf("Times[%d] = %s, want %s", i, got.Times[i], want)
		}
	}

	// Values stay against their own hour, and the hour a model is silent for
	// reads NaN rather than zero — LightGBM treats those differently.
	assertMerged(t, got, "early__rain", []float64{1, 2, math.NaN()})
	assertMerged(t, got, "late__rain", []float64{math.NaN(), 20, 30})
}

func assertMerged(t *testing.T, forecast *Forecast, name string, want []float64) {
	t.Helper()
	got, ok := forecast.Columns[name]
	if !ok {
		t.Fatalf("column %s missing", name)
	}
	for i := range want {
		if math.IsNaN(want[i]) {
			if !math.IsNaN(got[i]) {
				t.Errorf("column %s[%d] = %v, want NaN", name, i, got[i])
			}
			continue
		}
		if got[i] != want[i] {
			t.Errorf("column %s[%d] = %v, want %v", name, i, got[i], want[i])
		}
	}
}

// Two messages for the same model would otherwise have one silently overwrite
// the other, halving the feature vector without any signal.
func TestMergeRejectsARepeatedColumn(t *testing.T) {
	column := map[string][]float64{"ecmwf_ifs__rain": {1}}
	model := modelForecast{
		name:    "ecmwf_ifs",
		times:   []time.Time{time.Unix(3600, 0).UTC()},
		columns: column,
	}

	if _, err := merge([]modelForecast{model, model}); err == nil {
		t.Fatal("merge succeeded on a repeated column, want error")
	}
}

// stubHours is short on purpose: these payloads exercise decode's error paths,
// and a realistic 48-hour window would only make the failures slower to read.
const stubHours = 3

// stubResponse builds a size-prefixed WeatherApiResponse by hand, so that
// responses the live API never sends can still be put in front of decode.
type stubResponse struct {
	model     openmeteo.Model
	start     int64
	end       int64
	interval  int32
	variables int
	values    int
	base      float32
	noHourly  bool
}

func newStub(model openmeteo.Model) stubResponse {
	const start = 1_755_547_200 // 2025-08-18T20:00:00Z
	return stubResponse{
		model:     model,
		start:     start,
		end:       start + stubHours*3600,
		interval:  3600,
		variables: len(forecastVariables),
		values:    stubHours,
	}
}

// value is what variable v reads at hour h — distinct per slot, so a column
// landing under the wrong key or the wrong hour is visible in the assertion.
func (s stubResponse) value(variable, hour int) float32 {
	return s.base + float32(variable*100+hour)
}

func (s stubResponse) build(t *testing.T) []byte {
	t.Helper()
	builder := flatbuffers.NewBuilder(0)

	response := func(hourly flatbuffers.UOffsetT, withHourly bool) []byte {
		openmeteo.WeatherApiResponseStart(builder)
		openmeteo.WeatherApiResponseAddModel(builder, s.model)
		if withHourly {
			openmeteo.WeatherApiResponseAddHourly(builder, hourly)
		}
		openmeteo.FinishSizePrefixedWeatherApiResponseBuffer(
			builder, openmeteo.WeatherApiResponseEnd(builder),
		)
		return builder.FinishedBytes()
	}

	if s.noHourly {
		return response(0, false)
	}

	// Nested tables have to be finished before the vector holding them is
	// started, so the variables are built first and their offsets kept.
	offsets := make([]flatbuffers.UOffsetT, s.variables)
	for i := range offsets {
		openmeteo.VariableWithValuesStartValuesVector(builder, s.values)
		for j := s.values - 1; j >= 0; j-- {
			builder.PrependFloat32(s.value(i, j))
		}
		values := builder.EndVector(s.values)

		openmeteo.VariableWithValuesStart(builder)
		openmeteo.VariableWithValuesAddValues(builder, values)
		offsets[i] = openmeteo.VariableWithValuesEnd(builder)
	}

	openmeteo.VariablesWithTimeStartVariablesVector(builder, len(offsets))
	for i := len(offsets) - 1; i >= 0; i-- {
		builder.PrependUOffsetT(offsets[i])
	}
	variables := builder.EndVector(len(offsets))

	openmeteo.VariablesWithTimeStart(builder)
	openmeteo.VariablesWithTimeAddTime(builder, s.start)
	openmeteo.VariablesWithTimeAddTimeEnd(builder, s.end)
	openmeteo.VariablesWithTimeAddInterval(builder, s.interval)
	openmeteo.VariablesWithTimeAddVariables(builder, variables)

	return response(openmeteo.VariablesWithTimeEnd(builder), true)
}
