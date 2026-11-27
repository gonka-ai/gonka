package hostrpc

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"common/httpguard"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/registry"
	"devshard/transport"
)

type dialsHeld []registry.HostDial

func (d dialsHeld) HostDials() []registry.HostDial { return d }

type waveSink struct {
	waves chan map[string]*transport.RPCStatsSnapshot
}

func newWaveSink() waveSink {
	return waveSink{waves: make(chan map[string]*transport.RPCStatsSnapshot, 16)}
}

func (s waveSink) Replace(snapshots map[string]*transport.RPCStatsSnapshot) {
	select {
	case s.waves <- snapshots:
	default:
	}
}

func runnable() config.HostRPCStats {
	return config.HostRPCStats{IntervalMS: 10_000, TimeoutMS: 5_000, Concurrency: 2}
}

func allowLoopback(t *testing.T, allow bool) {
	t.Helper()
	previous := httpguard.AllowPrivate()
	httpguard.SetAllowPrivate(allow)
	t.Cleanup(func() { httpguard.SetAllowPrivate(previous) })
}

func statsServer(t *testing.T, minute int64, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if r.URL.Path != "/devshard/stats/rpc" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(transport.RPCStatsSnapshot{MinuteUnix: minute, Host: transport.RPCStatsHost{Requests: 7, Banned: 2}})
	}))
	t.Cleanup(server.Close)
	return server
}

func waitWave(t *testing.T, sink waveSink) map[string]*transport.RPCStatsSnapshot {
	t.Helper()
	select {
	case wave := <-sink.waves:
		return wave
	case <-time.After(5 * time.Second):
		t.Fatal("no wave reached the sink within five seconds")
		return nil
	}
}

// Test flow:
//  1. Build settings that are runnable and mark them disabled.
//  2. Call New with those settings, one live host dial, and a sink.
//  3. Assert it returns nil.
//  4. Repeat with a timeout longer than the interval and assert nil again.
func TestAPollerIsAbsentWhenPollingIsOffOrTheScheduleDoesNotHold(t *testing.T) {
	live := dialsHeld{{ParticipantKey: "gonka1a", BaseURL: "http://a:8080"}}
	off := runnable()
	off.Disabled = true
	if New(off, live, newWaveSink()) != nil {
		t.Error("New() returned a poller for polling that is switched off")
	}
	slow := config.HostRPCStats{IntervalMS: 10, TimeoutMS: 20, Concurrency: 1}
	if New(slow, live, newWaveSink()) != nil {
		t.Error("New() accepted a poll that can outlast its own period")
	}
}

// Test flow:
//  1. Start two loopback hosts, one answering the stats route and one answering 500, with the dial guard opened for loopback.
//  2. Build a poller over both and run one wave.
//  3. Read the wave from the sink.
//  4. Assert the answering host carries its snapshot and the failing host is present with a nil snapshot.
func TestAWaveReportsEachLiveHostAndMarksTheOneThatDidNotAnswer(t *testing.T) {
	allowLoopback(t, true)
	good := statsServer(t, 1_700_000_040, nil)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(broken.Close)
	sink := newWaveSink()
	poller := New(runnable(), dialsHeld{
		{ParticipantKey: "gonka1good", BaseURL: good.URL + "/"},
		{ParticipantKey: "gonka1broken", BaseURL: broken.URL},
	}, sink)

	poller.wave(t.Context())

	wave := waitWave(t, sink)
	if len(wave) != 2 {
		t.Fatalf("wave holds %d hosts, want both live hosts", len(wave))
	}
	if snapshot := wave["gonka1good"]; snapshot == nil || snapshot.MinuteUnix != 1_700_000_040 || snapshot.Host.Banned != 2 {
		t.Errorf("good host snapshot = %+v, want the served minute and its two refusals", snapshot)
	}
	if snapshot, present := wave["gonka1broken"]; !present || snapshot != nil {
		t.Errorf("broken host = %+v (present %v), want present with no snapshot", snapshot, present)
	}
}

// Test flow:
//  1. Start a loopback host that answers the stats route with a gzip body and the matching Content-Encoding.
//  2. Run one wave over it.
//  3. Assert the snapshot was decoded.
func TestAGzipBodyIsDecoded(t *testing.T) {
	allowLoopback(t, true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var body bytes.Buffer
		compressed := gzip.NewWriter(&body)
		_ = json.NewEncoder(compressed).Encode(transport.RPCStatsSnapshot{MinuteUnix: 60})
		_ = compressed.Close()
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(body.Bytes())
	}))
	t.Cleanup(server.Close)
	sink := newWaveSink()
	poller := New(runnable(), dialsHeld{{ParticipantKey: "gonka1gz", BaseURL: server.URL}}, sink)

	poller.wave(t.Context())

	if snapshot := waitWave(t, sink)["gonka1gz"]; snapshot == nil || snapshot.MinuteUnix != 60 {
		t.Fatalf("snapshot = %+v, want the gzip body decoded", snapshot)
	}
}

// Test flow:
//  1. Start a loopback host that answers with a valid snapshot padded with whitespace past the cap.
//  2. Run one wave over it.
//  3. Assert the host is reported with no snapshot.
func TestABodyOverTheCapIsRefused(t *testing.T) {
	allowLoopback(t, true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(append([]byte(`{"minute_unix":60}`), bytes.Repeat([]byte(" "), maxBodyBytes)...))
	}))
	t.Cleanup(server.Close)
	sink := newWaveSink()
	poller := New(runnable(), dialsHeld{{ParticipantKey: "gonka1big", BaseURL: server.URL}}, sink)

	poller.wave(t.Context())

	if snapshot := waitWave(t, sink)["gonka1big"]; snapshot != nil {
		t.Fatalf("snapshot = %+v, want none for an oversize body", snapshot)
	}
}

// Test flow:
//  1. Start one loopback host that counts its hits.
//  2. Run one wave over three dials, two of them under the same participant key.
//  3. Assert the sink got two hosts and the stub saw two polls, so one participant is polled once.
func TestOneParticipantIsPolledOncePerWave(t *testing.T) {
	allowLoopback(t, true)
	var hits atomic.Int32
	server := statsServer(t, 60, &hits)
	sink := newWaveSink()
	poller := New(runnable(), dialsHeld{
		{ParticipantKey: "gonka1a", BaseURL: server.URL},
		{ParticipantKey: "gonka1a", BaseURL: server.URL + "/"},
		{ParticipantKey: "gonka1b", BaseURL: server.URL},
	}, sink)

	poller.wave(t.Context())

	if wave := waitWave(t, sink); len(wave) != 2 {
		t.Errorf("wave holds %d hosts, want 2", len(wave))
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("polls = %d, want 2", got)
	}
}

// Test flow:
//  1. Start a loopback host that counts the requests in flight, reports each arrival, and holds every request until released.
//  2. Release all requests once two have arrived, from a goroutine bounded by five seconds.
//  3. Run one wave over six participants at that host with a concurrency of two.
//  4. Assert every participant was polled and no more than two requests were ever in flight.
func TestAWavePollsNoMoreHostsAtOnceThanItsConcurrency(t *testing.T) {
	allowLoopback(t, true)
	var inFlight, peak, served atomic.Int32
	arrived := make(chan struct{}, 6)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := inFlight.Add(1)
		for {
			seen := peak.Load()
			if now <= seen || peak.CompareAndSwap(seen, now) {
				break
			}
		}
		arrived <- struct{}{}
		<-release
		inFlight.Add(-1)
		served.Add(1)
		_ = json.NewEncoder(w).Encode(transport.RPCStatsSnapshot{})
	}))
	t.Cleanup(server.Close)
	go func() {
		defer close(release)
		for range 2 {
			select {
			case <-arrived:
			case <-time.After(5 * time.Second):
				return
			}
		}
	}()
	dials := make(dialsHeld, 0, 6)
	for _, key := range []string{"a", "b", "c", "d", "e", "f"} {
		dials = append(dials, registry.HostDial{ParticipantKey: "gonka1" + key, BaseURL: server.URL})
	}
	sink := newWaveSink()
	poller := New(config.HostRPCStats{IntervalMS: 10_000, TimeoutMS: 5_000, Concurrency: 2}, dials, sink)

	poller.wave(t.Context())

	if got := served.Load(); got != 6 {
		t.Errorf("polls = %d, want 6", got)
	}
	if got := peak.Load(); got > 2 {
		t.Errorf("peak in flight = %d, want at most the concurrency of 2", got)
	}
	_ = waitWave(t, sink)
}

// Test flow:
//  1. Start a loopback host that counts its hits and close the dial guard to loopback.
//  2. Run one wave over a dial to that host.
//  3. Assert the host is reported with no snapshot and the stub was never reached.
func TestAPollDoesNotDialAHostOnAPrivateAddress(t *testing.T) {
	allowLoopback(t, false)
	var hits atomic.Int32
	server := statsServer(t, 60, &hits)
	sink := newWaveSink()
	poller := New(runnable(), dialsHeld{{ParticipantKey: "gonka1local", BaseURL: server.URL}}, sink)

	poller.wave(t.Context())

	if snapshot, present := waitWave(t, sink)["gonka1local"]; !present || snapshot != nil {
		t.Errorf("host = %+v (present %v), want present with no snapshot", snapshot, present)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("stub hits = %d, want the guard to refuse the dial", got)
	}
}

// Test flow:
//  1. Start a loopback host and a poller on a ten-second interval, and start its loop under a cancellable context.
//  2. Read the first wave from the sink.
//  3. Cancel the context and wait up to five seconds for the loop to return.
//  4. Assert the loop returned.
func TestThePollerStopsWhenItsContextIsCancelled(t *testing.T) {
	allowLoopback(t, true)
	server := statsServer(t, 60, nil)
	sink := newWaveSink()
	poller := New(runnable(), dialsHeld{{ParticipantKey: "gonka1a", BaseURL: server.URL}}, sink)
	ctx, cancel := context.WithCancel(t.Context())
	returned := make(chan struct{})
	go func() {
		poller.run(ctx)
		close(returned)
	}()

	_ = waitWave(t, sink)
	cancel()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the poller loop did not return within five seconds of cancel")
	}
}
