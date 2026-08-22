package forecast

import (
	"context"
	"sync"
	"time"
)

// In-process TTL cache in front of Client.Fetch. Not keyed on anything: a
// Client addresses one fixed location and window.

// Open-Meteo refreshes the forecast endpoint at most hourly, so a short
// in-process TTL avoids hammering them during e.g. frontend reload loops
// without serving meaningfully stale data.
const cacheTTL = 600 * time.Second

// cache holds the last successful fetch and hands it back until it ages out.
type cache struct {
	ttl time.Duration

	// now is the clock, a seam for tests. time.Now readings carry a monotonic
	// component, so an entry's age survives a wall-clock jump.
	now func() time.Time

	// mu guards the entry, and is held only long enough to read or replace it
	// — never across a fetch.
	mu       sync.Mutex
	forecast *Forecast
	storedAt time.Time

	// fetching admits one caller at a time to the miss path. Concurrent
	// callers on a cold start all want the same forecast, so the ones that
	// lose take the winner's result rather than each asking Open-Meteo for
	// their own copy. A channel rather than a second mutex because waiting on
	// it has to be abandonable when the caller's context ends.
	fetching chan struct{}
}

func newCache(ttl time.Duration) *cache {
	return &cache{ttl: ttl, now: time.Now, fetching: make(chan struct{}, 1)}
}

// get returns the cached forecast if it is still fresh, and otherwise what
// fetch produces, storing it. A failed fetch is not cached — and does not
// evict what is there, though an entry that survives is by definition already
// stale, so it will only be served if a later fetch succeeds and replaces it.
func (c *cache) get(ctx context.Context, fetch func(context.Context) (*Forecast, error)) (*Forecast, error) {
	if forecast, ok := c.fresh(); ok {
		return forecast, nil
	}

	select {
	case c.fetching <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.fetching }()

	// The wait, if there was one, may have been spent on exactly the fetch
	// this caller was about to make.
	if forecast, ok := c.fresh(); ok {
		return forecast, nil
	}

	forecast, err := fetch(ctx)
	if err != nil {
		return nil, err
	}
	c.store(forecast)
	return forecast, nil
}

// fresh returns the entry if it is younger than the TTL.
func (c *cache) fresh() (*Forecast, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.forecast == nil || c.now().Sub(c.storedAt) >= c.ttl {
		return nil, false
	}
	return c.forecast, true
}

func (c *cache) store(forecast *Forecast) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.forecast, c.storedAt = forecast, c.now()
}
