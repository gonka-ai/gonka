package main

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/stub"
	"devshard/types"
	"devshard/user"
)

// The pairwise comparison reads the duration of each attempt from buildInvolvement. It must be the
// time from send until that attempt ended, not the time until the request settled.
func TestAttemptDurationIsMeasuredWhenTheAttemptEnds(t *testing.T) {
	e := &Redundancy{}
	// Both attempts were sent a second ago, so the request settles long after either of them ended.
	// The end times are given, not measured, so the test does not depend on the scheduler.
	t0 := time.Now().Add(-time.Second)
	a := &inflight{hostIdx: 0, nonce: 1, sendTime: t0, done: make(chan struct{})}
	b := &inflight{hostIdx: 1, nonce: 2, sendTime: t0.Add(50 * time.Millisecond), done: make(chan struct{})}

	// A really takes 100 ms, B really takes 400 ms.
	a.markEnded(t0.Add(100 * time.Millisecond))
	b.markEnded(t0.Add(450 * time.Millisecond))
	close(a.done)
	close(b.done)

	ha := e.buildInvolvement(a, 1, user.InferenceParams{})
	hb := e.buildInvolvement(b, 1, user.InferenceParams{})

	if ha.TotalTimeMs != 100 {
		t.Errorf("attempt A took 100 ms, recorded %.0f ms", ha.TotalTimeMs)
	}
	if hb.TotalTimeMs != 400 {
		t.Errorf("attempt B took 400 ms, recorded %.0f ms", hb.TotalTimeMs)
	}
	if ha.TotalTimeMs >= hb.TotalTimeMs {
		t.Errorf("attempt A is faster than B but is recorded as slower or equal (%.0f ms vs %.0f ms)", ha.TotalTimeMs, hb.TotalTimeMs)
	}
}

func TestAttemptDurationFallsBackToElapsedTimeWhenTheEndWasNeverRecorded(t *testing.T) {
	sent := time.Now().Add(-300 * time.Millisecond)
	inf := &inflight{sendTime: sent}
	got := inf.attemptDuration(sent.Add(300 * time.Millisecond))
	if got != 300*time.Millisecond {
		t.Fatalf("attempt that never ended: got %v, want 300ms", got)
	}
}

func TestAttemptEndIsRecordedOnce(t *testing.T) {
	sent := time.Now()
	inf := &inflight{sendTime: sent}
	inf.markEnded(sent.Add(100 * time.Millisecond))
	inf.markEnded(sent.Add(900 * time.Millisecond))
	if got := inf.attemptDuration(sent.Add(5 * time.Second)); got != 100*time.Millisecond {
		t.Fatalf("duration after a second markEnded: got %v, want 100ms", got)
	}
}

// The performance sample of an attempt carries the same duration as its involvement record: the time
// until the attempt ended, not the time until the sample was recorded.
func TestPerformanceSamplesUseTheAttemptDuration(t *testing.T) {
	cases := []struct {
		name   string
		record func(e *Redundancy, inf *inflight)
	}{
		{"ordinary sample", func(e *Redundancy, inf *inflight) {
			e.recordSample(inf, user.InferenceParams{}, true)
		}},
		{"winner failure after content", func(e *Redundancy, inf *inflight) {
			e.recordPostContentWinnerFailureOnce(inf, user.InferenceParams{})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			perf := NewPerfTracker(nil)
			e := &Redundancy{perf: perf}
			// Sent a second ago, ended after 100 ms, recorded now.
			sent := time.Now().Add(-time.Second)
			inf := &inflight{hostIdx: 0, nonce: 1, sendTime: sent, done: make(chan struct{})}
			inf.markEnded(sent.Add(100 * time.Millisecond))

			tc.record(e, inf)

			stats := perf.Stats(0)
			if stats.TotalSamples != 1 {
				t.Fatalf("expected one recorded sample, got %d", stats.TotalSamples)
			}
			if stats.AvgTotalTimeMs != 100 {
				t.Fatalf("sample duration is %.0f ms, want 100 ms (the attempt's own duration)", stats.AvgTotalTimeMs)
			}
		})
	}
}

// gatedReceiptClient holds the receipt callback of its inner client until gate is closed, so a test
// decides when the attempt on this host can end instead of waiting on a timer.
type gatedReceiptClient struct {
	inner user.HostClient
	gate  chan struct{}
}

func (c *gatedReceiptClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	wrapped := receiptHandler
	if wrapped != nil {
		wrapped = func(resp *host.HostResponse) {
			select {
			case <-c.gate:
			case <-ctx.Done():
			}
			receiptHandler(resp)
		}
	}
	return c.inner.Send(ctx, req, stream, wrapped)
}

// timedClient records when the Send call on its host started and when it returned, so a test can compare
// a recorded duration with the interval it actually observed.
type timedClient struct {
	inner user.HostClient

	mu       sync.Mutex
	entered  time.Time
	returned time.Time
}

func (c *timedClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	c.mu.Lock()
	if c.entered.IsZero() {
		c.entered = time.Now()
	}
	c.mu.Unlock()
	resp, err := c.inner.Send(ctx, req, stream, receiptHandler)
	c.mu.Lock()
	c.returned = time.Now()
	c.mu.Unlock()
	return resp, err
}

func (c *timedClient) interval() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.returned.Sub(c.entered)
}

// A full request through the real race. One host holds its receipt until the test releases it, so the
// request is won by another attempt while the slow attempt is still running. The request is settled only
// when the slow attempt is done. The winner must be recorded with its own duration, not with the time
// until the slow attempt ended.
func TestRunInferenceRecordsEachAttemptDurationAtItsOwnEnd(t *testing.T) {
	setSpeculativeTiming(t, 10*time.Millisecond, time.Second, 10*time.Millisecond, time.Minute)

	numHosts := 3
	hostSigners := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := types.SessionConfig{
		RefusalTimeout:   1,
		ExecutionTimeout: 1,
		TokenPrice:       1,
		VoteThreshold:    uint32(numHosts) / 2,
	}
	verifier := signing.NewSecp256k1Verifier()

	const slowHost = 1
	const holdAfterWinner = 200 * time.Millisecond
	gate := make(chan struct{})
	var releaseGate sync.Once
	release := func() { releaseGate.Do(func() { close(gate) }) }
	clients := make([]user.HostClient, numHosts)
	timed := make([]*timedClient, numHosts)
	for i := range hostSigners {
		sm := statetest.MustStateMachine(t, "escrow-proxy", config, group, 1_000_000, userKey.Address(), verifier)
		h, err := host.NewHost(sm, hostSigners[i], stub.NewInferenceEngine(), "escrow-proxy", group, nil, host.WithGrace(100))
		require.NoError(t, err)
		vc := &verifierClient{
			killableClient: &killableClient{inner: &user.InProcessClient{Host: h}},
			accept:         true,
			signer:         hostSigners[i],
			group:          group,
			slotIdx:        i,
		}
		var base user.HostClient = vc
		if i == slowHost {
			base = &gatedReceiptClient{inner: vc, gate: gate}
		}
		timed[i] = &timedClient{inner: base}
		clients[i] = timed[i]
	}

	userSM := statetest.MustStateMachine(t, "escrow-proxy", config, group, 1_000_000, userKey.Address(), verifier)
	session, err := user.NewSession(userSM, userKey, "escrow-proxy", group, clients, verifier)
	require.NoError(t, err)
	perf := NewPerfTracker(nil)
	redundancy := NewRedundancy(session, perf, numHosts, "llama")
	// Release the held attempt before stopping, so Stop does not wait for it when an assertion below
	// ends the test early. Registered after setSpeculativeTiming, so it runs before the settings are restored.
	t.Cleanup(func() {
		release()
		redundancy.Stop()
	})

	var buf bytes.Buffer
	require.NoError(t, redundancy.RunInference(context.Background(), defaultParams(), &buf, nil))

	// The winner has settled and its attempt has ended. The slow attempt is still held, so it ends
	// at least holdAfterWinner later, however long the scheduler makes the sleep.
	time.Sleep(holdAfterWinner)
	release()

	require.Eventually(t, func() bool { return len(perf.RecentRequests()) >= 1 }, 5*time.Second, 10*time.Millisecond,
		"the request should be recorded once every attempt is done")
	rec := perf.RecentRequests()[0]
	require.GreaterOrEqual(t, len(rec.Hosts), 2, "the slow attempt must have been joined by a second one")

	var winner, slow *HostInvolvement
	for i := range rec.Hosts {
		h := &rec.Hosts[i]
		if h.HostIdx == slowHost {
			slow = h
		} else if h.Winner {
			winner = h
		}
	}
	require.NotNil(t, winner, "a host other than the slow one must have won")
	require.NotNil(t, slow, "the slow attempt must be recorded")
	// Each recorded duration is compared with the interval the test observed around that host's Send call.
	// The recorded value covers at least that interval, because the send time is stamped before the call and
	// the end is recorded after it. The winner returned before the slow attempt was released, which is at
	// least holdAfterWinner before the request settled; a duration taken at settlement would exceed its own
	// interval by more than that.
	winnerSpan := timed[winner.HostIdx].interval()
	slowSpan := timed[slow.HostIdx].interval()
	require.GreaterOrEqual(t, winner.TotalTimeMs, float64(winnerSpan.Milliseconds()))
	require.LessOrEqual(t, winner.TotalTimeMs, float64((winnerSpan + holdAfterWinner/2).Milliseconds()),
		"the winner's recorded duration must not include the wait for the slow attempt")
	require.GreaterOrEqual(t, slow.TotalTimeMs, float64(slowSpan.Milliseconds()))
	require.GreaterOrEqual(t, slow.TotalTimeMs, float64(holdAfterWinner.Milliseconds()),
		"the slow attempt was held for at least holdAfterWinner after the winner returned")
}
