package features

import (
	"fmt"
	"time"

	"github.com/AndyHolt/will-it-rain/backend/internal/forecast"
)

// PickAnchor returns the hour to predict from: the latest forecast hour at or
// before now, floored to the hour.
//
// The label horizon is forward-looking, so anchoring on the last completed hour
// puts the start of the prediction window at now and its end four hours out.
// Anchoring an hour either side would shift every lagged feature with it, which
// is invisible in the assembled vector and wrong in all seventy values.
func PickAnchor(f *forecast.Forecast, now time.Time) (time.Time, error) {
	// Truncate measures from the zero time, so on a UTC instant it lands on
	// the hour — the same boundary pandas' floor("h") picks.
	floor := now.UTC().Truncate(time.Hour)

	row, found := rowAt(f, floor)
	if !found {
		// A miss gives the insertion point, so the row before it is the latest
		// hour that is still at or before now.
		row--
	}
	if row < 0 {
		return time.Time{}, fmt.Errorf(
			"forecast covers %s and has no hour at or before %s",
			describeRange(f), floor.Format(time.RFC3339),
		)
	}
	return f.Times[row], nil
}
