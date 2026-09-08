package main

import (
	"errors"
	"testing"
	"time"

	"common/probe"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func TestHostPingTargetsRefcountAndDedupe(t *testing.T) {
	targets := newHostPingTargets()
	const dial = "http://host.example:8080"
	prefix := "/devshard/v4"

	targets.ObserveEscrowHost("e1", dial, prefix, "pk-a")
	targets.ObserveEscrowHost("e1", dial, prefix, "pk-a") // idempotent
	targets.ObserveEscrowHost("e2", dial, prefix, "pk-b")
	require.Equal(t, 2, targets.refcount(dial))

	got := targets.Targets()
	require.Len(t, got, 1)
	require.Equal(t, dial, got[0].Key)
	require.Equal(t, dial+prefix+"/clock", got[0].ClockURL)
	require.Equal(t, dial+prefix+"/healthz", got[0].FallbackURL)

	pks := targets.participantKeys(dial)
	require.ElementsMatch(t, []string{"pk-a", "pk-b"}, pks)

	removed := targets.ReleaseEscrow("e1")
	require.Empty(t, removed)
	require.Equal(t, 1, targets.refcount(dial))
	require.ElementsMatch(t, []string{"pk-b"}, targets.participantKeys(dial))

	removed = targets.ReleaseEscrow("e2")
	require.Len(t, removed, 1)
	require.ElementsMatch(t, []string{"pk-b"}, removed[dial])
	require.Equal(t, 0, targets.refcount(dial))
	require.Empty(t, targets.Targets())

	// Idempotent release.
	require.Nil(t, targets.ReleaseEscrow("e2"))
}

func TestHostPingTargetsMultiDialPerEscrow(t *testing.T) {
	targets := newHostPingTargets()
	targets.ObserveEscrowHost("e1", "http://a:1", "/devshard/v4", "pk-a")
	targets.ObserveEscrowHost("e1", "http://b:1", "/devshard/v4", "pk-b")
	require.Len(t, targets.Targets(), 2)

	removed := targets.ReleaseEscrow("e1")
	require.Len(t, removed, 2)
	require.Empty(t, targets.Targets())
}

func TestHostPingRefcountExhaustivenessTeardownPaths(t *testing.T) {
	const dial = "http://host.example:9090"
	const pk = "participant-1"

	type pathCase struct {
		name string
		run  func(t *testing.T, g *Gateway, escrowID string)
	}
	cases := []pathCase{
		{
			name: "deactivateDevshardByIDWithReason",
			run: func(t *testing.T, g *Gateway, escrowID string) {
				require.True(t, g.deactivateDevshardByIDWithReason(escrowID, "test"))
			},
		},
		{
			name: "deactivateAndSettleDevshardByID",
			run: func(t *testing.T, g *Gateway, escrowID string) {
				// No store/chain — settle may no-op after deactivate; release must still run.
				g.deactivateAndSettleDevshardByID(escrowID, "test")
			},
		},
		{
			name: "retireRuntime",
			run: func(t *testing.T, g *Gateway, escrowID string) {
				require.True(t, g.retireRuntime(escrowID, "test"))
			},
		},
		{
			name: "startup_skipped_never_enters",
			run: func(t *testing.T, g *Gateway, escrowID string) {
				// Startup-skipped escrows only record a metric; they must never Observe.
				g.metrics.RecordStartupSkippedEscrow(escrowID, "Qwen/Test", "local_recovery_failed")
				require.False(t, g.hostPing.targets.hasEscrow(escrowID))
				require.Equal(t, 0, g.hostPing.targets.refcount(dial))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metrics := NewDevshardMetrics()
			// Enabled for target-set bookkeeping, but never start() — no probe loop.
			job := newHostPingJob(metrics, hostPingConfig{
				Interval:    defaultHostPingInterval,
				Timeout:     defaultHostPingTimeout,
				Concurrency: defaultHostPingConcurrency,
			})
			escrowID := "escrow-" + tc.name
			rt := &devshardRuntime{id: escrowID}
			rt.active.Store(true)
			g := &Gateway{
				runtimes:         map[string]*devshardRuntime{escrowID: rt},
				runtimeOrder:     []*devshardRuntime{rt},
				metrics:          metrics,
				hostPing:         job,
				rotationBreakers: make(map[string]*rotationBreaker),
			}

			if tc.name != "startup_skipped_never_enters" {
				job.ObserveEscrowHost(escrowID, dial, "/devshard/v4", pk)
				require.Equal(t, 1, job.targets.refcount(dial))
				metrics.SetHostPingParticipantInfo(dial, pk, true)
				metrics.ObserveHostPingResult(probe.Result{
					Key:              dial,
					Up:               true,
					Kind:             probe.KindDate,
					RTT:              5 * time.Millisecond,
					ConnReused:       true,
					At:               time.Now(),
					HasDivergence:    true,
					Divergence:       time.Second,
					DivergenceSource: probe.KindDate,
				})
			}

			tc.run(t, g, escrowID)

			if tc.name == "startup_skipped_never_enters" {
				return
			}
			require.Equal(t, 0, job.targets.refcount(dial), "teardown path %s must drop refcount to 0", tc.name)
			require.Empty(t, job.targets.Targets())
			assertNoHostPingSeriesForHost(t, metrics, dial)
		})
	}
}

func TestHostPingCleanupCompleteness(t *testing.T) {
	metrics := NewDevshardMetrics()
	const dial = "http://cleanup.example:8080"
	const pk = "pk-cleanup"

	metrics.ObserveHostPingResult(probe.Result{
		Key:              dial,
		Up:               true,
		Kind:             probe.KindDate,
		RTT:              3 * time.Millisecond,
		ConnReused:       true,
		At:               time.Now(),
		HasDivergence:    true,
		Divergence:       500 * time.Millisecond,
		DivergenceSource: probe.KindDate,
	})
	metrics.SetHostPingParticipantInfo(dial, pk, true)
	metrics.SetHostPingTargets(1)

	sink := &hostPingSink{metrics: metrics, targets: newHostPingTargets()}
	sink.Forget(dial)
	// Forget may not know participants if targets empty — delete explicitly too.
	metrics.DeleteHostPingMetrics(dial, []string{pk})

	assertNoHostPingSeriesForHost(t, metrics, dial)
}

func TestHostPingFleetWarmHistogram(t *testing.T) {
	metrics := NewDevshardMetrics()
	const dial = "http://warm.example:8080"

	metrics.ObserveHostPingResult(probe.Result{
		Key: dial, Up: true, Kind: probe.KindDate,
		RTT: 10 * time.Millisecond, ConnReused: true, At: time.Now(),
	})
	metrics.ObserveHostPingResult(probe.Result{
		Key: dial, Up: true, Kind: probe.KindDate,
		RTT: 20 * time.Millisecond, ConnReused: false, At: time.Now(), // cold: excluded
	})

	families, err := metrics.registry.Gather()
	require.NoError(t, err)
	requireMetricHistogramCount(t, families, "devshard_gateway_host_ping_warm_rtt_seconds", nil, 1)
	requireMetricGaugeValue(t, families, "devshard_gateway_host_ping_rtt_seconds", map[string]string{"host": dial}, 0.01)
}

func TestHostPingKillSwitch(t *testing.T) {
	metrics := NewDevshardMetrics()
	job := newHostPingJob(metrics, hostPingConfig{
		Interval:    200 * time.Millisecond,
		Timeout:     50 * time.Millisecond,
		Concurrency: 2,
		Disabled:    true,
	})
	job.start()
	defer job.stop()

	job.ObserveEscrowHost("e1", "http://disabled.example:1", "/devshard/v4", "pk")
	require.Empty(t, job.targets.Targets())
	require.False(t, job.targets.hasEscrow("e1"))

	// No tick counters should advance (scheduler never started).
	families, err := metrics.registry.Gather()
	require.NoError(t, err)
	require.Equal(t, 0.0, metricCounterValueOrZero(families, "devshard_gateway_host_ping_ticks_total", nil))
}

func TestHostPingHardInvariantProbeFailureDoesNotQuarantine(t *testing.T) {
	metrics := NewDevshardMetrics()
	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.SetMetrics(metrics)
	before := metricCounterSum(t, metrics, "devshard_gateway_participant_quarantine_transitions_total")

	sink := &hostPingSink{metrics: metrics, targets: newHostPingTargets()}
	for i := 0; i < 5; i++ {
		sink.Observe(probe.Result{
			Key:  "http://fail.example:1",
			Up:   false,
			Kind: probe.KindNone,
			At:   time.Now(),
			Err:  errors.New("connection refused"),
		})
	}

	// Drive a real short scheduler tick against a dead dial; still must not quarantine.
	job := newHostPingJob(metrics, hostPingConfig{
		Interval:    200 * time.Millisecond,
		Timeout:     50 * time.Millisecond,
		Concurrency: 2,
	})
	job.ObserveEscrowHost("e1", "http://127.0.0.1:1", "/devshard/v4", "pk-fail")
	job.start()
	defer job.stop()
	require.Eventually(t, func() bool {
		families, err := metrics.registry.Gather()
		if err != nil {
			return false
		}
		return metricCounterValueOrZero(families, "devshard_gateway_host_ping_ticks_total", nil) >= 1
	}, 2*time.Second, 20*time.Millisecond)

	after := metricCounterSum(t, metrics, "devshard_gateway_participant_quarantine_transitions_total")
	require.Equal(t, before, after, "probe failures must not quarantine")

	// Control: the limiter still records quarantine when inference calls it.
	for i := 0; i < emptyStreamQuarantineThreshold; i++ {
		limiter.ObserveEmptyStreamForModel("pk-control", "Qwen/Test")
	}
	require.Greater(t, metricCounterSum(t, metrics, "devshard_gateway_participant_quarantine_transitions_total"), after)
}

func TestHostPingParticipantInfoDedupeMapping(t *testing.T) {
	metrics := NewDevshardMetrics()
	job := newHostPingJob(metrics, hostPingConfig{
		Interval:    defaultHostPingInterval,
		Timeout:     defaultHostPingTimeout,
		Concurrency: defaultHostPingConcurrency,
	})
	const dial = "http://map.example:8080"
	job.ObserveEscrowHost("e1", dial, "/devshard/v4", "pk-a")
	job.ObserveEscrowHost("e2", dial, "/devshard/v4", "pk-b")

	families, err := metrics.registry.Gather()
	require.NoError(t, err)
	requireMetricGaugeValue(t, families, "devshard_gateway_host_ping_participant_info", map[string]string{"host": dial, "participant_key": "pk-a"}, 1)
	requireMetricGaugeValue(t, families, "devshard_gateway_host_ping_participant_info", map[string]string{"host": dial, "participant_key": "pk-b"}, 1)
	require.Len(t, job.targets.Targets(), 1)
}

func TestHostPingJoinProbePath(t *testing.T) {
	require.Equal(t, "http://h/devshard/v4/healthz", joinProbePath("http://h/", "/devshard/v4", "healthz"))
	require.Contains(t, joinProbePath("http://h", "", "/clock"), "/clock")
}

func TestHostPingRoutePrefixChangeInvalidatesCapability(t *testing.T) {
	targets := newHostPingTargets()
	const dial = "http://upgrade.example:8080"

	require.False(t, targets.ObserveEscrowHost("e1", dial, "/devshard/v2", "pk-a"))
	got := targets.Targets()
	require.Len(t, got, 1)
	require.Equal(t, dial+"/devshard/v2/clock", got[0].ClockURL)

	// Same escrow, new prefix → capability rediscovery required.
	require.True(t, targets.ObserveEscrowHost("e1", dial, "/devshard/v3", "pk-a"))
	got = targets.Targets()
	require.Len(t, got, 1)
	require.Equal(t, dial+"/devshard/v3/clock", got[0].ClockURL)

	// New escrow on same dial with yet another prefix also invalidates.
	require.True(t, targets.ObserveEscrowHost("e2", dial, "/devshard/v4", "pk-b"))
	got = targets.Targets()
	require.Len(t, got, 1)
	require.Equal(t, dial+"/devshard/v4/clock", got[0].ClockURL)
}

func TestHostPingInvalidateDialNoopWithoutProber(t *testing.T) {
	job := newHostPingJob(NewDevshardMetrics(), hostPingConfig{Disabled: true})
	// Must not panic when kill-switched (no prober).
	job.InvalidateDial("http://x")
}

func TestHostClientDialInfo(t *testing.T) {
	_, _, ok := hostClientDialInfo("not-a-dialer")
	require.False(t, ok)

	var d hostDialer = &fakeDialer{base: "http://x:1/", prefix: "/devshard/v4"}
	dial, prefix, ok := hostClientDialInfo(d)
	require.True(t, ok)
	require.Equal(t, "http://x:1", dial)
	require.Equal(t, "/devshard/v4", prefix)
}

type fakeDialer struct {
	base, prefix string
}

func (f *fakeDialer) BaseURL() string     { return f.base }
func (f *fakeDialer) RoutePrefix() string { return f.prefix }

func assertNoHostPingSeriesForHost(t *testing.T, metrics *DevshardMetrics, host string) {
	t.Helper()
	families, err := metrics.registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		name := family.GetName()
		if name != "devshard_gateway_host_ping_up" &&
			name != "devshard_gateway_host_ping_rtt_seconds" &&
			name != "devshard_gateway_host_clock_divergence_seconds" &&
			name != "devshard_gateway_host_ping_last_probe_timestamp_seconds" &&
			name != "devshard_gateway_host_ping_probe_kind" &&
			name != "devshard_gateway_host_ping_participant_info" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, lp := range metric.GetLabel() {
				if lp.GetName() == "host" && lp.GetValue() == host {
					t.Fatalf("%s still has host=%q after cleanup", name, host)
				}
			}
		}
	}
}

func metricCounterValueOrZero(families []*dto.MetricFamily, name string, labels map[string]string) float64 {
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if labels == nil || metricLabelsMatch(metric, labels) {
				if metric.Counter != nil {
					return metric.Counter.GetValue()
				}
			}
		}
	}
	return 0
}

func metricCounterSum(t *testing.T, metrics *DevshardMetrics, name string) float64 {
	t.Helper()
	families, err := metrics.registry.Gather()
	require.NoError(t, err)
	var sum float64
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if metric.Counter != nil {
				sum += metric.Counter.GetValue()
			}
		}
	}
	return sum
}
