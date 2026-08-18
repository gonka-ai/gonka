package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A draining instance must report unready without touching the chain: the
// answer no longer depends on chain reachability, and probing would only delay
// the very check the balancer is waiting on. The nil client is the assertion —
// any chain call here would panic.
func TestReadyz_ReportsDrainingWithoutProbingTheChain(t *testing.T) {
	r := newReadiness(nil)
	r.beginDrain()

	rec := serveReadyz(t, r)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"reason":"draining"`)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}

// Draining latches: readiness must not come back while the process is on its
// way out, or the balancer would route to it again mid-shutdown.
func TestReadyz_DrainingLatches(t *testing.T) {
	r := newReadiness(nil)
	r.beginDrain()

	for i := 0; i < 3; i++ {
		require.Equal(t, http.StatusServiceUnavailable, serveReadyz(t, r).Code)
	}
}

func TestBeginDrain_FlipsReadinessOnTheServer(t *testing.T) {
	srv := New(nil)
	srv.BeginDrain()

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"reason":"draining"`)
}

func TestReadyz_DrainWinsAgainstConcurrentChainProbe(t *testing.T) {
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	r := &readiness{probe: func(context.Context) error {
		close(probeStarted)
		<-releaseProbe
		return nil
	}}
	type result struct {
		response *httptest.ResponseRecorder
		err      error
	}
	response := make(chan result, 1)
	go func() {
		e := echo.New()
		recorder := httptest.NewRecorder()
		ctx := e.NewContext(httptest.NewRequest(http.MethodGet, "/readyz", nil), recorder)
		response <- result{response: recorder, err: r.handler(ctx)}
	}()

	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("readiness probe did not start")
	}
	r.beginDrain()
	close(releaseProbe)

	select {
	case got := <-response:
		require.NoError(t, got.err)
		assert.Equal(t, http.StatusServiceUnavailable, got.response.Code)
		assert.Contains(t, got.response.Body.String(), `"reason":"draining"`)
	case <-time.After(time.Second):
		t.Fatal("readiness request did not finish")
	}
}

func serveReadyz(t *testing.T, r *readiness) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/readyz", nil), rec)
	require.NoError(t, r.handler(c))
	return rec
}

// One expired cache plus N concurrent checks must cost one chain query, and the
// probe must run on its own clock: the answer is about the chain, not about how
// long any particular caller was willing to wait.
func TestCheck_ConcurrentCallersShareOneProbe(t *testing.T) {
	var probes atomic.Int32
	release := make(chan struct{})
	r := &readiness{probe: func(ctx context.Context) error {
		probes.Add(1)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}

	var wg sync.WaitGroup
	results := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = r.check()
		}(i)
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	require.EqualValues(t, 1, probes.Load(),
		"concurrent checks over one expired cache must share one probe")
	for i, err := range results {
		require.NoError(t, err, "caller %d must ride on the shared probe's answer", i)
	}
}

// A probe that hangs is bounded by the readiness budget, not by the caller: the
// check concludes "unreachable" on its own schedule and caches that, instead of
// inheriting whatever deadline the health checker happened to have.
func TestCheck_ProbeIsBoundedByItsOwnBudget(t *testing.T) {
	r := &readiness{probe: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}

	start := time.Now()
	err := r.check()
	elapsed := time.Since(start)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.GreaterOrEqual(t, elapsed, readinessTimeout-100*time.Millisecond)
	require.Less(t, elapsed, readinessTimeout+time.Second,
		"the probe must be cut at its own budget")

	// The verdict is cached: the next check answers immediately from the cache
	// rather than hanging on another probe.
	start = time.Now()
	require.Error(t, r.check())
	require.Less(t, time.Since(start), 100*time.Millisecond)
}
