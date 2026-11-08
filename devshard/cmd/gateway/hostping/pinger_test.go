package hostping

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"common/httpguard"
	"common/probe"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/registry"
)

type countingSink struct {
	targets chan int
	results chan probe.Result
}

func (s countingSink) Observe(result probe.Result) {
	select {
	case s.results <- result:
	default:
	}
}
func (s countingSink) Forget(string) {}
func (s countingSink) TickStarted()  {}
func (s countingSink) TickSkipped()  {}
func (s countingSink) TargetCount(count int) {
	select {
	case s.targets <- count:
	default:
	}
}

func runnableSettings() config.HostPing {
	return config.HostPing{IntervalMS: 40, TimeoutMS: 10, Concurrency: 2}
}

// Test flow:
//  1. Build runnable settings and mark them disabled.
//  2. Call New with those settings, one live host dial, and a counting sink.
//  3. Assert it returns nil.
func TestAPingerIsAbsentWhenTheProbeIsOff(t *testing.T) {
	settings := runnableSettings()
	settings.Disabled = true

	if pinger := New(settings, dialsHeld{{BaseURL: "http://a:8080"}}, countingSink{}); pinger != nil {
		t.Fatal("New() returned a pinger for a probe that is switched off")
	}
}

// Test flow:
//  1. Build settings whose wave could outlast its own period (interval and timeout both 10ms).
//  2. Call New with those settings, one live host dial, and a counting sink.
//  3. Assert it returns nil rather than a pinger.
func TestAScheduleThatDoesNotHoldCostsThePingNotTheBoot(t *testing.T) {
	settings := config.HostPing{IntervalMS: 10, TimeoutMS: 10, Concurrency: 1}

	if pinger := New(settings, dialsHeld{{BaseURL: "http://a:8080"}}, countingSink{}); pinger != nil {
		t.Fatal("New() accepted a wave that can outlast its own period")
	}
}

// Test flow:
//  1. Build a pinger over runnable settings, one live host dial, and a counting sink with a buffered channel.
//  2. Start the pinger.
//  3. Read the reported target count from the sink's channel.
//  4. Assert it counts the one live host.
func TestEachWaveReadsWhoIsLiveNow(t *testing.T) {
	sink := countingSink{targets: make(chan int, 1)}
	pinger := New(runnableSettings(), dialsHeld{
		{ParticipantKey: "gonka1abc", BaseURL: "http://127.0.0.1:1/"},
	}, sink)
	if pinger == nil {
		t.Fatal("New() returned nothing for a runnable schedule")
	}
	ctx := t.Context()

	pinger.Start(ctx)

	if got := <-sink.targets; got != 1 {
		t.Fatalf("targets = %d, want the one live host", got)
	}
}

var (
	_ liveDials = dialsHeld(nil)
	_           = registry.HostDial{}
)

// Test flow:
//  1. Start a loopback stub that counts hits and close the dial guard.
//  2. Build a pinger over two live host dials, one by the stub's IP and one by the host name localhost, and start it.
//  3. Read probe results from the sink until both hosts have reported, failing if they do not within five seconds.
//  4. Assert each result is down with the guard's refusal and the stub was never reached.
func TestAPingDoesNotDialAHostOnAPrivateAddress(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	previous := httpguard.AllowPrivate()
	httpguard.SetAllowPrivate(false)
	t.Cleanup(func() { httpguard.SetAllowPrivate(previous) })
	byName := "http://localhost:" + strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	sink := countingSink{results: make(chan probe.Result, 2)}
	pinger := New(runnableSettings(), dialsHeld{
		{ParticipantKey: "gonka1byip", BaseURL: server.URL},
		{ParticipantKey: "gonka1byname", BaseURL: byName},
	}, sink)
	if pinger == nil {
		t.Fatal("New() returned nothing for a runnable schedule")
	}
	ctx, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()

	pinger.Start(ctx)

	reported := map[string]bool{}
	for len(reported) < 2 {
		select {
		case result := <-sink.results:
			if result.Up || result.Err == nil || !strings.Contains(result.Err.Error(), "ssrf guard") {
				t.Fatalf("probe of %s = up %v, err %v, want down with the ssrf guard's refusal", result.Key, result.Up, result.Err)
			}
			reported[result.Key] = true
		case <-ctx.Done():
			t.Fatalf("hosts reported = %v, want gonka1byip and gonka1byname", reported)
		}
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("hits on the private host = %d, want 0", got)
	}
}
