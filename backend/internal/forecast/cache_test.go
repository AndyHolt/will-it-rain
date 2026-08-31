package forecast

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// clock is a hand-wound stand-in for time.Now, so that ageing an entry out
// costs no wall-clock time. Guarded because the concurrency test reads it from
// several goroutines at once.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock {
	return &clock{now: time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newTestCache returns a cache on a hand-wound clock, plus the clock.
func newTestCache() (*cache, *clock) {
	clk := newClock()
	c := newCache(cacheTTL)
	c.now = clk.Now
	return c, clk
}

// counting wraps a fetch, recording how many times it was actually called.
func counting(forecast *Forecast) (func(context.Context) (*Forecast, error), *atomic.Int64) {
	var calls atomic.Int64
	return func(context.Context) (*Forecast, error) {
		calls.Add(1)
		return forecast, nil
	}, &calls
}

func TestCacheServesTheStoredForecastUntilItAgesOut(t *testing.T) {
	stored := &Forecast{}
	fetch, calls := counting(stored)
	c, clk := newTestCache()

	first, err := c.get(context.Background(), fetch)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}

	// One second short of the TTL is still a hit: the boundary is where an
	// off-by-one turns a 10-minute cache into a per-request fetch.
	clk.advance(cacheTTL - time.Second)
	second, err := c.get(context.Background(), fetch)
	if err != nil {
		t.Fatalf("second get: %v", err)
	}

	if second != first {
		t.Error("get returned a different forecast inside the TTL, want the stored one")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("fetched %d times inside the TTL, want 1", got)
	}
}

func TestCacheRefetchesOnceTheEntryIsStale(t *testing.T) {
	fetch, calls := counting(&Forecast{})
	c, clk := newTestCache()

	if _, err := c.get(context.Background(), fetch); err != nil {
		t.Fatalf("first get: %v", err)
	}
	clk.advance(cacheTTL)
	if _, err := c.get(context.Background(), fetch); err != nil {
		t.Fatalf("second get: %v", err)
	}

	if got := calls.Load(); got != 2 {
		t.Errorf("fetched %d times across the TTL boundary, want 2", got)
	}
}

// A cached failure would outlive the blip that caused it, turning one bad
// response into ten minutes of them.
func TestCacheDoesNotStoreAFailedFetch(t *testing.T) {
	wanted := errors.New("open-meteo is down")
	c, _ := newTestCache()

	if _, err := c.get(context.Background(), func(context.Context) (*Forecast, error) {
		return nil, wanted
	}); !errors.Is(err, wanted) {
		t.Fatalf("get returned %v, want the fetch's own error", err)
	}

	stored := &Forecast{}
	got, err := c.get(context.Background(), func(context.Context) (*Forecast, error) {
		return stored, nil
	})
	if err != nil {
		t.Fatalf("get after a failure: %v", err)
	}
	if got != stored {
		t.Error("get did not retry after a failed fetch")
	}
}

// The alternative — serving the last good forecast when a refresh fails — is
// worse here than an error: the caller's prediction would silently be for an
// hour that has already passed, with nothing in the response to say so.
func TestCacheDoesNotServeAStaleEntryWhenTheRefreshFails(t *testing.T) {
	fetch, _ := counting(&Forecast{})
	c, clk := newTestCache()

	if _, err := c.get(context.Background(), fetch); err != nil {
		t.Fatalf("first get: %v", err)
	}
	clk.advance(cacheTTL)

	wanted := errors.New("open-meteo is down")
	forecast, err := c.get(context.Background(), func(context.Context) (*Forecast, error) {
		return nil, wanted
	})
	if !errors.Is(err, wanted) {
		t.Errorf("get returned %v, want the failed refresh's error", err)
	}
	if forecast != nil {
		t.Error("get returned the aged-out forecast, want nothing")
	}
}

// A cold start answers several queued requests at once, and they all want the
// same hour of forecast. One request to Open-Meteo, not one each.
func TestCacheCollapsesConcurrentMisses(t *testing.T) {
	const callers = 8

	var calls atomic.Int64
	entered := make(chan struct{}, callers)
	release := make(chan struct{})
	stored := &Forecast{}
	c, _ := newTestCache()

	fetch := func(context.Context) (*Forecast, error) {
		calls.Add(1)
		// Held open until every caller has reached get, so the losers are
		// waiting on the fetch rather than arriving after it.
		<-release
		return stored, nil
	}

	var group sync.WaitGroup
	got := make([]*Forecast, callers)
	for i := range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			entered <- struct{}{}
			forecast, err := c.get(context.Background(), fetch)
			if err != nil {
				t.Errorf("concurrent get: %v", err)
				return
			}
			got[i] = forecast
		}()
	}

	for range callers {
		<-entered
	}
	close(release)
	group.Wait()

	if n := calls.Load(); n != 1 {
		t.Errorf("%d concurrent callers made %d fetches, want 1", callers, n)
	}
	for i, forecast := range got {
		if forecast != stored {
			t.Errorf("caller %d got %v, want the single fetched forecast", i, forecast)
		}
	}
}

// Waiting behind someone else's fetch still has to end when the caller's
// request does — the deadline is the budget whether it is spent fetching or
// queuing.
func TestCacheAbandonsTheWaitWhenTheCallerIsCancelled(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	c, _ := newTestCache()

	go func() {
		_, _ = c.get(context.Background(), func(context.Context) (*Forecast, error) {
			close(started)
			<-release
			return &Forecast{}, nil
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.get(ctx, func(context.Context) (*Forecast, error) {
		t.Error("get fetched on a cancelled context, want it to give up waiting")
		return &Forecast{}, nil
	}); !errors.Is(err, context.Canceled) {
		t.Errorf("get returned %v, want context.Canceled", err)
	}
}

// The cache is wired into Fetch, not just implemented next to it.
func TestFetchSkipsOpenMeteoWhileTheForecastIsFresh(t *testing.T) {
	fixture := readTestdata(t, "forecast.fb")
	var requests atomic.Int64
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if _, err := w.Write(fixture); err != nil {
			t.Errorf("writing stub response: %v", err)
		}
	})

	first, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	second, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}

	if second != first {
		t.Error("the second Fetch decoded a fresh forecast, want the cached one")
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("Open-Meteo was asked %d times, want 1", got)
	}
}
