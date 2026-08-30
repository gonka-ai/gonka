package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Finalize settles the session at the current nonce, so it may only run once the
// runtime is quiet. Checking the drain barrier once and then serving finalize is
// not enough: admission only looks at rt.active, which stays true for the whole
// finalize call (it flips in markDevshardInactiveAfterFinalize, after the 2xx),
// so a chat request can be admitted right behind the check and end up executing
// against a session that is being finalized. The finalizing gate closes that
// window by marking the runtime in the same g.mu critical section that observes
// it as quiet.

// blockingFinalizeRuntime returns a runtime whose handler parks inside finalize
// until the returned release func is called, plus a signal fired on entry.
func blockingFinalizeRuntime(t *testing.T, id string, status int) (*devshardRuntime, <-chan struct{}, func()) {
	t.Helper()
	entered := make(chan struct{})
	unblock := make(chan struct{})
	signalEntered := sync.OnceFunc(func() { close(entered) })
	rt := &devshardRuntime{
		id:    id,
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			signalEntered()
			<-unblock
			w.WriteHeader(status)
		}),
	}
	return rt, entered, sync.OnceFunc(func() { close(unblock) })
}

// TestFinalizeInFlightBlocksNewInferences pins the TOCTOU: while the finalize
// handler runs, both admission routes must refuse the runtime and no reservation
// may be taken.
func TestFinalizeInFlightBlocksNewInferences(t *testing.T) {
	rt, entered, release := blockingFinalizeRuntime(t, "12", http.StatusNoContent)
	defer release()
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")

	finalized := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		g.handleDevshard(rec, httptest.NewRequest(http.MethodPost, "/devshard/12/v1/finalize", nil))
		finalized <- rec.Code
	}()
	<-entered

	ok, reason := rt.acceptsNewInferences()
	require.False(t, ok, "a runtime with finalize in flight must not accept new inferences")
	require.Equal(t, "finalize_in_flight", reason)

	_, err := g.reserveRuntimeForModel("Qwen/Test", 1)
	require.Error(t, err, "pooled admission must not pick a runtime whose finalize is in flight")

	admitted, reason := g.reserveRuntimeIfAccepting(rt, 1)
	require.False(t, admitted, "direct devshard admission must not reserve a runtime whose finalize is in flight")
	require.Equal(t, "finalize_in_flight", reason)
	require.Zero(t, rt.activeUserRequests.Load(), "no request may be reserved while finalize runs")

	release()
	require.Equal(t, http.StatusNoContent, <-finalized)
	require.False(t, rt.active.Load(), "a successful finalize leaves the runtime inactive")
}

// TestSingleOnlyFinalizeInFlightBlocksNewInferences pins the same window on the
// single-runtime route, which shares the pooled admission path.
func TestSingleOnlyFinalizeInFlightBlocksNewInferences(t *testing.T) {
	rt, entered, release := blockingFinalizeRuntime(t, "12", http.StatusOK)
	defer release()
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")

	finalized := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		g.handleSingleOnly(rec, httptest.NewRequest(http.MethodPost, "/v1/finalize", nil))
		finalized <- rec.Code
	}()
	<-entered

	_, err := g.reserveRuntimeForModel("Qwen/Test", 1)
	require.Error(t, err, "single-runtime admission must not pick a runtime whose finalize is in flight")
	require.Zero(t, rt.activeUserRequests.Load())

	release()
	require.Equal(t, http.StatusOK, <-finalized)
}

// TestSingleOnlyFinalizeRequiresNoActiveRequests pins the drain-barrier check on
// the single-runtime route: finalize is refused, not forwarded, while a request
// is in flight.
func TestSingleOnlyFinalizeRequiresNoActiveRequests(t *testing.T) {
	var forwarded bool
	rt := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwarded = true
			w.WriteHeader(http.StatusOK)
		}),
	}
	rt.activeUserRequests.Store(1)
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")

	rec := httptest.NewRecorder()
	g.handleSingleOnly(rec, httptest.NewRequest(http.MethodPost, "/v1/finalize", nil))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.False(t, forwarded)
	require.False(t, rt.finalizing.Load(), "a refused finalize must leave the gate open")

	rt.activeUserRequests.Store(0)
	rec = httptest.NewRecorder()
	g.handleSingleOnly(rec, httptest.NewRequest(http.MethodPost, "/v1/finalize", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, forwarded)
	require.False(t, rt.finalizing.Load())
}

// TestFailedFinalizeReopensAdmission pins the clearing rule: a finalize that did
// not succeed must leave the runtime usable for new inferences.
func TestFailedFinalizeReopensAdmission(t *testing.T) {
	rt := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":{"message":"finalize failed"}}`, http.StatusInternalServerError)
		}),
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")

	rec := httptest.NewRecorder()
	g.handleDevshard(rec, httptest.NewRequest(http.MethodPost, "/devshard/12/v1/finalize", nil))
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	require.True(t, rt.active.Load(), "a failed finalize must not deactivate the runtime")
	require.False(t, rt.finalizing.Load(), "a failed finalize must reopen admission")
	ok, _ := rt.acceptsNewInferences()
	require.True(t, ok)
}

// TestPanickingFinalizeReopensAdmission pins the same rule for a handler that
// panics: the gate is cleared by defer, so the runtime is not stuck closed.
func TestPanickingFinalizeReopensAdmission(t *testing.T) {
	rt := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("finalize exploded")
		}),
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")

	func() {
		defer func() { require.NotNil(t, recover(), "handler panic must propagate") }()
		g.handleDevshard(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/devshard/12/v1/finalize", nil))
	}()

	require.False(t, rt.finalizing.Load(), "a panicking finalize must reopen admission")
	ok, _ := rt.acceptsNewInferences()
	require.True(t, ok)
}

// TestConcurrentFinalizeIsRefused pins the double-finalize case: the second
// finalize must be refused outright, not queued on finalizeMu. A queued one
// would run after the first finalize's defer reopened the gate, so it would
// forward with admission open again -- the very window this change closes.
func TestConcurrentFinalizeIsRefused(t *testing.T) {
	rt, entered, release := blockingFinalizeRuntime(t, "12", http.StatusNoContent)
	defer release()
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")

	first := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		g.handleDevshard(rec, httptest.NewRequest(http.MethodPost, "/devshard/12/v1/finalize", nil))
		first <- rec.Code
	}()
	<-entered

	second := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		g.handleDevshard(rec, httptest.NewRequest(http.MethodPost, "/devshard/12/v1/finalize", nil))
		second <- rec
	}()
	select {
	case rec := <-second:
		require.Equal(t, http.StatusConflict, rec.Code, "a second finalize must be refused while the first is in flight")
		require.Contains(t, rec.Body.String(), "already has a finalize in flight", "the refusal must name the real reason")
	case <-time.After(30 * time.Second):
		t.Fatal("the second finalize was queued behind the first instead of being refused")
	}

	release()
	require.Equal(t, http.StatusNoContent, <-first)
}

// TestFailedFinalizeAllowsRetry pins the refusal semantics end to end: after a
// finalize fails, the gate is open again -- admission reserves, and a retried
// finalize is served instead of being refused as a duplicate.
func TestFailedFinalizeAllowsRetry(t *testing.T) {
	var calls atomic.Int64
	rt := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				http.Error(w, `{"error":{"message":"finalize failed"}}`, http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")

	rec := httptest.NewRecorder()
	g.handleDevshard(rec, httptest.NewRequest(http.MethodPost, "/devshard/12/v1/finalize", nil))
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	admitted, reason := g.reserveRuntimeIfAccepting(rt, 1)
	require.True(t, admitted, "a failed finalize must reopen admission, refused with %q", reason)
	g.releaseRuntime(rt, 1)

	rec = httptest.NewRecorder()
	g.handleDevshard(rec, httptest.NewRequest(http.MethodPost, "/devshard/12/v1/finalize", nil))
	require.Equal(t, http.StatusNoContent, rec.Code, "a retried finalize must not be refused as a duplicate")
	require.EqualValues(t, 2, calls.Load(), "the retried finalize must reach the runtime")
	require.False(t, rt.active.Load(), "the successful retry leaves the runtime inactive")
}
