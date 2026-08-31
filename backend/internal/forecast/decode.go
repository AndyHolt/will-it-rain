package forecast

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/AndyHolt/will-it-rain/backend/internal/openmeteo"
)

// decode parses an Open-Meteo FlatBuffers payload into the canonical column map.
//
// A multi-model response is a *sequence* of size-prefixed WeatherApiResponse
// messages — one per model, concatenated — not a single root. Iterate the
// buffer; parsing it once silently yields only the first model, which reads as
// half the feature vector having gone missing.
func decode(payload []byte) (*Forecast, error) {
	var models []modelForecast
	for offset := 0; offset < len(payload); {
		size, err := messageSize(payload, offset)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", len(models), err)
		}
		model, err := decodeModel(payload, offset)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", len(models), err)
		}
		models = append(models, model)
		offset += size
	}
	if len(models) == 0 {
		return nil, errors.New("payload carried no responses")
	}
	return merge(models)
}

// messageSize returns how many bytes the size-prefixed message at offset
// occupies, prefix included.
func messageSize(payload []byte, offset int) (int, error) {
	remaining := len(payload) - offset
	if remaining < flatbuffers.SizeUint32 {
		return 0, fmt.Errorf("truncated size prefix: %d bytes remain", remaining)
	}
	prefix := int(flatbuffers.GetSizePrefix(payload, flatbuffers.UOffsetT(offset)))
	size := prefix + flatbuffers.SizeUint32
	if prefix <= 0 || size > remaining {
		return 0, fmt.Errorf("size prefix %d overruns the %d bytes that remain", prefix, remaining)
	}
	return size, nil
}

// modelForecast is one model's response, before the models are joined.
type modelForecast struct {
	name    string
	times   []time.Time
	columns map[string][]float64
}

func decodeModel(payload []byte, offset int) (modelForecast, error) {
	response := openmeteo.GetSizePrefixedRootAsWeatherApiResponse(
		payload, flatbuffers.UOffsetT(offset),
	)

	// The enum name is the model name, exactly as model_to_name resolves it
	// Python-side by reversing the same table.
	name, known := openmeteo.EnumNamesModel[response.Model()]
	if !known {
		return modelForecast{}, fmt.Errorf("unknown model code %d", response.Model())
	}

	hourly := response.Hourly(nil)
	if hourly == nil {
		return modelForecast{}, fmt.Errorf("model %s: response carried no hourly data", name)
	}

	times, err := hourlyTimes(hourly)
	if err != nil {
		return modelForecast{}, fmt.Errorf("model %s: %w", name, err)
	}

	// Variables come back positionally, in the order requested — which is why
	// the count has to match before indexing into them.
	if got := hourly.VariablesLength(); got != len(forecastVariables) {
		return modelForecast{}, fmt.Errorf(
			"model %s: response carried %d variables, %d were requested",
			name, got, len(forecastVariables),
		)
	}

	columns := make(map[string][]float64, len(forecastVariables))
	var variable openmeteo.VariableWithValues
	for i, requested := range forecastVariables {
		// Positional rather than matched on variable.Variable(), as the Python
		// client also is: the enum does not round-trip to the requested name.
		// "temperature_2m" arrives as `temperature` with Altitude 2, so
		// matching by enum would mean rebuilding Open-Meteo's suffix rules
		// here to recover a name we already know.
		if !hourly.Variables(&variable, i) {
			return modelForecast{}, fmt.Errorf("model %s: variable %s missing", name, requested)
		}
		if got := variable.ValuesLength(); got != len(times) {
			return modelForecast{}, fmt.Errorf(
				"model %s: variable %s carried %d values for %d hours",
				name, requested, got, len(times),
			)
		}

		values := make([]float64, len(times))
		for j := range values {
			// Widened, not reinterpreted. The Python path holds these as
			// float32 too, so parity depends on not inventing precision here.
			values[j] = float64(variable.Values(j))
		}
		columns[name+"__"+requested] = values
	}

	return modelForecast{name: name, times: times, columns: columns}, nil
}

// hourlyTimes expands Time/TimeEnd/Interval into the half-open [Time, TimeEnd)
// index, matching the pandas date_range the Python fetcher builds with
// inclusive="left".
func hourlyTimes(hourly *openmeteo.VariablesWithTime) ([]time.Time, error) {
	interval := int64(hourly.Interval())
	if interval <= 0 {
		return nil, fmt.Errorf("interval %ds is not positive", interval)
	}
	start, end := hourly.Time(), hourly.TimeEnd()
	if end < start {
		return nil, fmt.Errorf("time range ends (%d) before it starts (%d)", end, start)
	}

	times := make([]time.Time, 0, (end-start)/interval)
	for t := start; t < end; t += interval {
		times = append(times, time.Unix(t, 0).UTC())
	}
	return times, nil
}

// merge outer-joins the per-model columns on time, as the Python fetcher's
// merge(on="date", how="outer") does.
//
// Both models normally return the same 48 hours, but nothing in the API
// guarantees it — a model publishing on a different cadence contributes NaN
// for the hours it lacks, which is what LightGBM expects for missing input
// anyway.
func merge(models []modelForecast) (*Forecast, error) {
	row := make(map[int64]int)
	for _, model := range models {
		for _, t := range model.times {
			row[t.Unix()] = 0
		}
	}

	stamps := make([]int64, 0, len(row))
	for stamp := range row {
		stamps = append(stamps, stamp)
	}
	slices.Sort(stamps)

	times := make([]time.Time, len(stamps))
	for i, stamp := range stamps {
		row[stamp] = i
		times[i] = time.Unix(stamp, 0).UTC()
	}

	columns := make(map[string][]float64, len(models)*len(forecastVariables))
	for _, model := range models {
		for name, values := range model.columns {
			if _, clash := columns[name]; clash {
				return nil, fmt.Errorf("two responses both carried column %s", name)
			}
			column := make([]float64, len(times))
			for i := range column {
				column[i] = math.NaN()
			}
			for j, t := range model.times {
				column[row[t.Unix()]] = values[j]
			}
			columns[name] = column
		}
	}

	return &Forecast{Times: times, Columns: columns}, nil
}
