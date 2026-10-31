package perf

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"devshard/cmd/gateway/config"
)

func testPerf() config.Perf {
	return config.Perf{
		EWMAHalfLifeSeconds:      600,
		ConsecutiveFailThreshold: 3,
		FailureRateThreshold:     0.5,
		FailureRateMinVolume:     5,
		EjectionBaseSeconds:      30,
		EjectionMaxSeconds:       600,
		MaxEjectionFraction:      0.5,
		MinAvailableHosts:        1,
		HostStalenessSeconds:     60,
	}
}

func newTestTracker(perf config.Perf, now func() time.Time) *Tracker {
	return NewTracker(config.NewHolder(&config.Config{Perf: perf}), now)
}

func fixedNow(instant time.Time) func() time.Time {
	return func() time.Time { return instant }
}

func failAllConsecutive(tracker *Tracker, participant, model string, count int64) {
	for range count {
		tracker.RecordSample(Sample{ParticipantKey: participant, Model: model, Responsive: false})
	}
}

// noCap disables the max-ejection cap so a lone host's own trigger can be observed in isolation.
func noCap(perf config.Perf) config.Perf {
	perf.MaxEjectionFraction = 1.0
	perf.MinAvailableHosts = 0
	return perf
}

// Test flow:
//  1. Build a tracker with the max-ejection cap disabled.
//  2. Drive participant-a/model-a to consecutive failures at the threshold.
//  3. Assert `Ejected` reports true.
func TestTrackerEjectedTrueAfterConsecutiveFailThreshold(t *testing.T) {
	perf := noCap(testPerf())
	tracker := newTestTracker(perf, fixedNow(testEpoch))

	failAllConsecutive(tracker, "participant-a", "model-a", perf.ConsecutiveFailThreshold)

	if !tracker.Ejected("participant-a", "model-a") {
		t.Fatal("Ejected() after reaching the consecutive-fail threshold = false, want true")
	}
}

// Test flow:
//  1. Build a tracker with the max-ejection cap disabled.
//  2. Drive participant-a/model-a to one failure short of the consecutive-fail threshold.
//  3. Assert `Ejected` reports false.
func TestTrackerEjectedFalseBeforeConsecutiveFailThreshold(t *testing.T) {
	perf := noCap(testPerf())
	tracker := newTestTracker(perf, fixedNow(testEpoch))

	failAllConsecutive(tracker, "participant-a", "model-a", perf.ConsecutiveFailThreshold-1)

	if tracker.Ejected("participant-a", "model-a") {
		t.Fatal("Ejected() below the consecutive-fail threshold = true, want false")
	}
}

// Test flow:
//  1. Build a tracker.
//  2. Assert `Ejected` reports false for a participant/model pair that was never recorded.
func TestTrackerEjectedFalseForNeverRecordedHost(t *testing.T) {
	tracker := newTestTracker(testPerf(), fixedNow(testEpoch))

	if tracker.Ejected("never-seen", "model-a") {
		t.Fatal("Ejected() for a never-recorded host = true, want false")
	}
}

// Test flow:
//  1. Build a tracker with a 10-second ejection base, a consecutive-fail threshold of 1, and the cap disabled.
//  2. Record one failing sample and assert `Ejected` reports true immediately.
//  3. Advance the clock 9 seconds and assert `Ejected` is still true, within the window.
//  4. Advance the clock 2 more seconds (11s total) and assert `Ejected` is now false, past the window.
func TestTrackerEjectedRespectsConfiguredEjectionBaseSeconds(t *testing.T) {
	perf := noCap(testPerf())
	perf.EjectionBaseSeconds = 10
	perf.ConsecutiveFailThreshold = 1
	current := testEpoch
	tracker := newTestTracker(perf, func() time.Time { return current })

	tracker.RecordSample(Sample{ParticipantKey: "participant-a", Model: "model-a", Responsive: false})
	if !tracker.Ejected("participant-a", "model-a") {
		t.Fatal("Ejected() right after the trigger = false, want true")
	}

	current = current.Add(9 * time.Second)
	if !tracker.Ejected("participant-a", "model-a") {
		t.Fatal("Ejected() 9s into a 10s ejection base = false, want true (still within window)")
	}

	current = current.Add(2 * time.Second)
	if tracker.Ejected("participant-a", "model-a") {
		t.Fatal("Ejected() 11s into a 10s ejection base = true, want false (window elapsed)")
	}
}

// Test flow:
//  1. Build a tracker with a max-ejection fraction of 0.5 and min-available-hosts of 1.
//  2. Drive p2 and p3 to a first round of consecutive failures.
//  3. Once the ejection window elapses, drive all of p0-p3 to a second round of consecutive failures.
//  4. Assert p2 and p3, ejected twice each, still report as ejected.
//  5. Assert p0 and p1, ejected once each, are pardoned by the ejection cap.
func TestTrackerEjectedCapKeepsTheMostChronicHostsEjected(t *testing.T) {
	perf := testPerf()
	perf.MaxEjectionFraction = 0.5
	perf.MinAvailableHosts = 1
	instant := testEpoch
	tracker := newTestTracker(perf, func() time.Time { return instant })

	for _, participant := range []string{"p2", "p3"} {
		failAllConsecutive(tracker, participant, "model-a", perf.ConsecutiveFailThreshold)
	}
	instant = instant.Add(time.Duration(perf.EjectionBaseSeconds)*time.Second + time.Second)
	for _, participant := range []string{"p0", "p1", "p2", "p3"} {
		failAllConsecutive(tracker, participant, "model-a", perf.ConsecutiveFailThreshold)
	}

	if !tracker.Ejected("p2", "model-a") || !tracker.Ejected("p3", "model-a") {
		t.Fatal("expected p2 and p3, ejected twice each, to remain reported as ejected")
	}
	if tracker.Ejected("p0", "model-a") || tracker.Ejected("p1", "model-a") {
		t.Fatal("expected p0 and p1, ejected once each, to be pardoned by the ejection cap")
	}
}

// Test flow:
//  1. Build a tracker with a max-ejection fraction of 0.5 and min-available-hosts of 1.
//  2. Drive p0-p3 to consecutive failures past the threshold.
//  3. Assert every participant still reports `Degraded` true: the detector's verdict survives the cap.
//  4. Assert p3 is pardoned from routing (`Ejected` false) even though it is degraded.
//  5. Assert `Degraded` is not keyed by model: p0 against a different model reports false.
func TestTrackerReportsCapPardonedHostsAsDegradedButNotEjected(t *testing.T) {
	perf := testPerf()
	perf.MaxEjectionFraction = 0.5
	perf.MinAvailableHosts = 1
	tracker := newTestTracker(perf, fixedNow(testEpoch))

	participants := []string{"p0", "p1", "p2", "p3"}
	for _, participant := range participants {
		failAllConsecutive(tracker, participant, "model-a", perf.ConsecutiveFailThreshold)
	}

	for _, participant := range participants {
		if !tracker.Degraded(participant, "model-a") {
			t.Fatalf("Degraded(%q) = false, want the detector's verdict before the cap", participant)
		}
	}
	if tracker.Ejected("p3", "model-a") {
		t.Fatal("p3 was withheld from routing, so the cap did not pardon it")
	}
	if tracker.Degraded("p0", "other-model") {
		t.Fatal("Degraded is not keyed by model")
	}
}

// Test flow:
//  1. Build a tracker with a max-ejection fraction of 0.5 and min-available-hosts of 1.
//  2. Drive p0-p3 to consecutive failures on model-a, and q0-q1 to consecutive failures on model-b.
//  3. Assert model-a's cap (min(0.5*4, 4-1) = 2) keeps its two lexicographically first hosts, p0 and p1, ejected and pardons p2 and p3.
//  4. Assert model-b's own cap (min(0.5*2, 2-1) = 1) keeps q0 ejected and pardons q1, independent of what model-a's cap spent.
func TestTrackerEjectedCapIsCountedPerModel(t *testing.T) {
	perf := testPerf()
	perf.MaxEjectionFraction = 0.5
	perf.MinAvailableHosts = 1
	tracker := newTestTracker(perf, fixedNow(testEpoch))

	for _, participant := range []string{"p0", "p1", "p2", "p3"} {
		failAllConsecutive(tracker, participant, "model-a", perf.ConsecutiveFailThreshold)
	}
	for _, participant := range []string{"q0", "q1"} {
		failAllConsecutive(tracker, participant, "model-b", perf.ConsecutiveFailThreshold)
	}

	if !tracker.Ejected("p0", "model-a") || !tracker.Ejected("p1", "model-a") {
		t.Fatal("expected model-a's two lexicographically first hosts to stay ejected")
	}
	if tracker.Ejected("p2", "model-a") || tracker.Ejected("p3", "model-a") {
		t.Fatal("expected model-a's cap of 2 to pardon p2 and p3")
	}
	if !tracker.Ejected("q0", "model-b") {
		t.Fatal("expected model-b's own cap of 1 to keep q0 ejected")
	}
	if tracker.Ejected("q1", "model-b") {
		t.Fatal("expected model-b's cap of 1 to pardon q1")
	}
}

// Test flow:
//  1. Build a tracker with max-ejection fraction 1.0 (which alone would allow ejecting all hosts) but min-available-hosts of 2.
//  2. Drive p0, p1, p2 to consecutive failures past the threshold.
//  3. Assert p0 (lexicographically first) stays ejected.
//  4. Assert p1 and p2 are pardoned: MinAvailableHosts=2 of 3 known hosts allows only 1 ejected.
func TestTrackerEjectedCapBoundByMinAvailableHosts(t *testing.T) {
	perf := testPerf()
	perf.MaxEjectionFraction = 1.0
	perf.MinAvailableHosts = 2
	tracker := newTestTracker(perf, fixedNow(testEpoch))

	participants := []string{"p0", "p1", "p2"}
	for _, participant := range participants {
		failAllConsecutive(tracker, participant, "model-a", perf.ConsecutiveFailThreshold)
	}

	if !tracker.Ejected("p0", "model-a") {
		t.Fatal("expected p0 (lexicographically first) to remain reported as ejected")
	}
	if tracker.Ejected("p1", "model-a") || tracker.Ejected("p2", "model-a") {
		t.Fatal("expected p1 and p2 to be pardoned: MinAvailableHosts=2 of 3 known hosts allows only 1 ejected")
	}
}

// Test flow:
//  1. Build a tracker with min-available-hosts of 1.
//  2. Drive the only known host, lone-host, to consecutive failures past the threshold.
//  3. Assert `Ejected` reports false: the min-available-hosts floor protects the only known host.
func TestTrackerEjectedCapNeverEjectsTheOnlyKnownHost(t *testing.T) {
	perf := testPerf()
	perf.MinAvailableHosts = 1
	tracker := newTestTracker(perf, fixedNow(testEpoch))

	failAllConsecutive(tracker, "lone-host", "model-a", perf.ConsecutiveFailThreshold)

	if tracker.Ejected("lone-host", "model-a") {
		t.Fatal("Ejected() for the only known host = true, want false (MinAvailableHosts floor)")
	}
}

// Test flow:
//  1. Build a tracker.
//  2. Record a tool-unsupported refusal for participant-a and a context limit for participant-b.
//  3. Assert `Capability` reports the tool refusal for participant-a and the limit plus context refusal for participant-b.
//  4. Assert `Capability` for an unrecorded participant-c reports nothing.
func TestTrackerCapabilityDelegatesToTheRefusalCounts(t *testing.T) {
	tracker := newTestTracker(testPerf(), fixedNow(testEpoch))

	tracker.RecordToolUnsupported("participant-a", capabilityModel)
	tracker.RecordContextLimit("participant-b", capabilityModel, 100)

	if _, _, toolRefusals, _ := tracker.Capability("participant-a", capabilityModel); toolRefusals != 1 {
		t.Errorf("tool refusals = %d, want the one recorded", toolRefusals)
	}
	if limit, _, _, contextRefusals := tracker.Capability("participant-b", capabilityModel); limit != 100 || contextRefusals != 1 {
		t.Errorf("limit/refusals = %d/%d, want 100 and one refusal", limit, contextRefusals)
	}
	if limit, version, tool, context := tracker.Capability("participant-c", capabilityModel); limit|version|tool|context != 0 {
		t.Errorf("an unknown participant reports %d/%d/%d/%d, want nothing", limit, version, tool, context)
	}
}

// Test flow:
//  1. Build a tracker and record one responsive sample for participant-a/model-a.
//  2. Acquire participant-a twice and assert its snapshot inflight count is 2.
//  3. Release once and assert the snapshot inflight count drops to 1.
func TestTrackerAcquireReleaseTracksInflight(t *testing.T) {
	tracker := newTestTracker(testPerf(), fixedNow(testEpoch))
	tracker.RecordSample(Sample{ParticipantKey: "participant-a", Model: "model-a", Responsive: true})

	tracker.Acquire("participant-a")
	tracker.Acquire("participant-a")
	if got := tracker.Snapshot()[0].Inflight; got != 2 {
		t.Fatalf("in flight after 2 acquires = %d, want 2", got)
	}

	tracker.Release("participant-a")
	if got := tracker.Snapshot()[0].Inflight; got != 1 {
		t.Fatalf("in flight after 1 release = %d, want 1", got)
	}
}

// Test flow:
//  1. Build a tracker.
//  2. Fill the latency window for "quick" (10ms decode latency) and "slow" (20ms decode latency), both on model qwen.
//  3. Assert the snapshot reports each participant's own TimePerOutputToken p75.
func TestSnapshotReportsTimePerOutputTokenP75PerPair(t *testing.T) {
	tracker := newTestTracker(testPerf(), fixedNow(testEpoch))
	for range latencyWindowMinimum {
		tracker.RecordSample(Sample{ParticipantKey: "quick", Model: "qwen", Responsive: true, TimePerOutputToken: 10 * time.Millisecond})
		tracker.RecordSample(Sample{ParticipantKey: "slow", Model: "qwen", Responsive: true, TimePerOutputToken: 20 * time.Millisecond})
	}

	states := map[string]HostState{}
	for _, state := range tracker.Snapshot() {
		states[state.Participant] = state
	}

	if got := states["quick"].TimePerOutputToken; got != 10*time.Millisecond {
		t.Errorf("quick decode p75 = %v, want 10ms", got)
	}
	if got := states["slow"].TimePerOutputToken; got != 20*time.Millisecond {
		t.Errorf("slow decode p75 = %v, want 20ms", got)
	}
}

// Test flow:
//  1. Build a tracker.
//  2. Record one fewer sample than the minimum window for a single host.
//  3. Assert the snapshot has exactly one tracked pair.
//  4. Assert that pair's TimePerOutputToken reports 0, matching `TimePerOutputTokenP75`'s own refusal below the minimum.
func TestSnapshotReportsZeroTimePerOutputTokenBelowTheMinimumWindow(t *testing.T) {
	tracker := newTestTracker(testPerf(), fixedNow(testEpoch))
	for range latencyWindowMinimum - 1 {
		tracker.RecordSample(Sample{ParticipantKey: "host", Model: "qwen", Responsive: true, TimePerOutputToken: 20 * time.Millisecond})
	}

	states := tracker.Snapshot()
	if len(states) != 1 {
		t.Fatalf("tracked pairs = %d, want 1", len(states))
	}
	if got := states[0].TimePerOutputToken; got != 0 {
		t.Errorf("decode p75 below the minimum window = %v, want 0", got)
	}
}

// Test flow:
//  1. Build a tracker with a 60-second host-staleness window.
//  2. Record one sample for stale-host and assert the hosts map holds 1 entry.
//  3. Advance the clock 61 seconds, past the staleness window, and record a sample for fresh-host.
//  4. Assert the hosts map still holds only 1 entry, and that it is fresh-host, not the swept stale-host.
func TestTrackerRecordSampleLazilyEvictsHostsUnseenPastStaleness(t *testing.T) {
	perf := testPerf()
	perf.HostStalenessSeconds = 60
	current := testEpoch
	tracker := newTestTracker(perf, func() time.Time { return current })

	tracker.RecordSample(Sample{ParticipantKey: "stale-host", Model: "model-a", Responsive: true})
	if got := len(tracker.hosts); got != 1 {
		t.Fatalf("hosts map len after the first sample = %d, want 1", got)
	}

	current = current.Add(61 * time.Second)
	tracker.RecordSample(Sample{ParticipantKey: "fresh-host", Model: "model-a", Responsive: true})

	if got := len(tracker.hosts); got != 1 {
		t.Fatalf("hosts map len after the stale sweep = %d, want 1 (only fresh-host)", got)
	}
	if _, exists := tracker.hosts[hostKey{participant: "fresh-host", model: "model-a"}]; !exists {
		t.Fatal("fresh-host missing from the hosts map after the sweep")
	}
	if got := len(tracker.hosts); got != 1 {
		t.Fatalf("hosts known for model-a after the sweep = %d, want 1 (the count the ejection cap divides)", got)
	}
}

// Test flow:
//  1. Build a tracker.
//  2. Across 50 iterations over 5 participants, launch concurrent goroutines that record samples, take snapshots, check ejection, and acquire/snapshot/release.
//  3. Wait for all goroutines to finish without the race detector reporting a conflict.
func TestTrackerConcurrentRecordAndQueryNoRace(t *testing.T) {
	tracker := newTestTracker(testPerf(), fixedNow(testEpoch))

	var wg sync.WaitGroup
	for i := range 50 {
		participant := fmt.Sprintf("participant-%d", i%5)
		wg.Add(4)
		go func() {
			defer wg.Done()
			tracker.RecordSample(Sample{ParticipantKey: participant, Model: "model-a", Responsive: true})
		}()
		go func() {
			defer wg.Done()
			tracker.Snapshot()
		}()
		go func() {
			defer wg.Done()
			tracker.Ejected(participant, "model-a")
		}()
		go func() {
			defer wg.Done()
			tracker.Acquire(participant)
			tracker.Snapshot()
			tracker.Release(participant)
		}()
	}
	wg.Wait()
}

// Test flow:
//  1. Build a tracker.
//  2. Record one fewer sample than the minimum window with a 20ms decode latency.
//  3. Assert `TimePerOutputTokenP75` reports unknown.
//  4. Record one more sample to fill the window.
//  5. Assert `TimePerOutputTokenP75` now reports known and returns 20ms.
func TestTimePerOutputTokenP75NeedsAFullEnoughWindow(t *testing.T) {
	tracker := newTestTracker(testPerf(), fixedNow(time.Unix(1700000000, 0)))

	for range latencyWindowMinimum - 1 {
		tracker.RecordSample(Sample{ParticipantKey: "host", Model: "qwen", Responsive: true, TimePerOutputToken: 20 * time.Millisecond})
	}
	if _, ok := tracker.TimePerOutputTokenP75("host", "qwen"); ok {
		t.Fatal("a quantile was reported before the window held enough samples")
	}

	tracker.RecordSample(Sample{ParticipantKey: "host", Model: "qwen", Responsive: true, TimePerOutputToken: 20 * time.Millisecond})

	got, ok := tracker.TimePerOutputTokenP75("host", "qwen")
	if !ok {
		t.Fatal("the window filled and still reported nothing")
	}
	if got != 20*time.Millisecond {
		t.Fatalf("TimePerOutputTokenP75() = %v, want 20ms", got)
	}
}

// Test flow:
//  1. Build a tracker.
//  2. Fill the latency window for "quick" (10ms decode latency) and "slow" (20ms decode latency), both on model qwen.
//  3. Assert `TimePerOutputTokenP75` reports 10ms for quick and 20ms for slow.
func TestTimePerOutputTokenP75SeparatesASlowDecoderFromAFastOne(t *testing.T) {
	tracker := newTestTracker(testPerf(), fixedNow(time.Unix(1700000000, 0)))

	for range latencyWindowMinimum {
		tracker.RecordSample(Sample{ParticipantKey: "quick", Model: "qwen", Responsive: true, TimePerOutputToken: 10 * time.Millisecond})
		tracker.RecordSample(Sample{ParticipantKey: "slow", Model: "qwen", Responsive: true, TimePerOutputToken: 20 * time.Millisecond})
	}

	quick, _ := tracker.TimePerOutputTokenP75("quick", "qwen")
	slow, _ := tracker.TimePerOutputTokenP75("slow", "qwen")

	if quick != 10*time.Millisecond || slow != 20*time.Millisecond {
		t.Fatalf("p75 quick = %v, slow = %v, want 10ms and 20ms", quick, slow)
	}
}

// Test flow:
//  1. Build a tracker.
//  2. Record enough measured samples with a 20ms decode latency to fill the window, then record four times as many unmeasured samples carrying no decode latency.
//  3. Assert `TimePerOutputTokenP75` still reports the measured 20ms rather than a folded-in zero.
func TestAnUnmeasuredDecodeIsNotFoldedIntoTheWindow(t *testing.T) {
	const measured, unmeasured = latencyWindowMinimum, 4 * latencyWindowMinimum
	tracker := newTestTracker(testPerf(), fixedNow(time.Unix(1700000000, 0)))

	for range measured {
		tracker.RecordSample(Sample{ParticipantKey: "host", Model: "qwen", Responsive: true, TimePerOutputToken: 20 * time.Millisecond})
	}
	for range unmeasured {
		tracker.RecordSample(Sample{ParticipantKey: "host", Model: "qwen", Responsive: true})
	}

	got, ok := tracker.TimePerOutputTokenP75("host", "qwen")

	if !ok || got != 20*time.Millisecond {
		t.Fatalf("TimePerOutputTokenP75() = (%v, %v), want (20ms, true): an unmeasured attempt entered the window", got, ok)
	}
}

// Test flow:
//  1. Build a capability tracker.
//  2. Record a version-unsupported refusal for participant-a twice.
//  3. Assert the first call reports itself as new and the repeat does not.
//  4. Record a tool-unsupported refusal for participant-a twice.
//  5. Assert the first call reports itself as new and the repeat does not.
func TestARefusalIsAnnouncedOnceRatherThanOnEveryRepeat(t *testing.T) {
	tracker := newCapabilityTracker()

	if first := tracker.recordVersionUnsupported("participant-a"); !first {
		t.Error("the first version refusal did not report itself as new")
	}
	if first := tracker.recordVersionUnsupported("participant-a"); first {
		t.Error("a repeated version refusal reported itself as new")
	}
	if first := tracker.recordToolUnsupported("participant-a", capabilityModel); !first {
		t.Error("the first tool refusal did not report itself as new")
	}
	if first := tracker.recordToolUnsupported("participant-a", capabilityModel); first {
		t.Error("a repeated tool refusal reported itself as new")
	}
}

// Test flow:
//  1. Build a capability tracker.
//  2. Record a context limit of 4096 for participant-a; assert it reports previous 0 and changed true.
//  3. Record the same 4096 limit again; assert it reports changed false.
//  4. Record a smaller 2048 limit; assert it reports previous 4096 and changed true.
func TestOnlyAChangedContextLimitIsAnnounced(t *testing.T) {
	tracker := newCapabilityTracker()

	if previous, changed := tracker.recordContextLimit("participant-a", capabilityModel, 4096); previous != 0 || !changed {
		t.Errorf("first limit reported previous=%d changed=%v, want 0 and true", previous, changed)
	}
	if _, changed := tracker.recordContextLimit("participant-a", capabilityModel, 4096); changed {
		t.Error("the same limit reported itself as a change")
	}
	if previous, changed := tracker.recordContextLimit("participant-a", capabilityModel, 2048); previous != 4096 || !changed {
		t.Errorf("a smaller limit reported previous=%d changed=%v, want 4096 and true", previous, changed)
	}
}

// Test flow:
//  1. For each case (first-content latency or decode latency, naming which dimension the table varies), fill the window with 1-second samples on that case's own latency field, then with 2-second samples.
//  2. Compute the tracker's `Pressure` for participant-a/model-a.
//  3. Assert the case's own dimension reports pressure above 1.5.
//  4. Assert the other dimension stays at 0, since no sample carried that latency.
func TestPressureKeepsEachLatencyInItsOwnDimension(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name      string
		sampleOf  func(latency time.Duration) Sample
		slowed    func(Pressure) float64
		untouched func(Pressure) float64
	}{
		{
			name: "first content",
			sampleOf: func(latency time.Duration) Sample {
				return Sample{ParticipantKey: "participant-a", Model: "model-a", Responsive: true, FirstContent: latency}
			},
			slowed:    func(measured Pressure) float64 { return measured.FirstContent },
			untouched: func(measured Pressure) float64 { return measured.Decode },
		},
		{
			name: "decode",
			sampleOf: func(latency time.Duration) Sample {
				return Sample{ParticipantKey: "participant-a", Model: "model-a", Responsive: true, TimePerOutputToken: latency}
			},
			slowed:    func(measured Pressure) float64 { return measured.Decode },
			untouched: func(measured Pressure) float64 { return measured.FirstContent },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			tracker := newTestTracker(testPerf(), fixedNow(time.Unix(1_700_000_000, 0)))
			for range latencyWindowSize {
				tracker.RecordSample(testCase.sampleOf(time.Second))
			}
			for range latencyWindowSize {
				tracker.RecordSample(testCase.sampleOf(2 * time.Second))
			}

			measured := tracker.Pressure("participant-a", "model-a")

			if slowed := testCase.slowed(measured); slowed <= 1.5 {
				t.Errorf("pressure on the slowed dimension = %v, want above 1.5: the host now answers at twice its own best", slowed)
			}
			if untouched := testCase.untouched(measured); untouched != 0 {
				t.Errorf("pressure on the other dimension = %v, want 0: no sample carried that latency", untouched)
			}
		})
	}
}

// Test flow:
//  1. Build a tracker with the max-ejection cap disabled.
//  2. Record served-stream mismatches for one host up to the consecutive-fail threshold.
//  3. Assert the host is ejected, exactly as the same run of stalls would eject it.
func TestServedMismatchesEjectAHostLikeStalls(t *testing.T) {
	perf := noCap(testPerf())
	tracker := newTestTracker(perf, fixedNow(testEpoch))

	for range perf.ConsecutiveFailThreshold {
		tracker.RecordServedMismatch("participant-a", "model-a")
	}

	if !tracker.Ejected("participant-a", "model-a") {
		t.Fatal("Ejected() after served mismatches at the consecutive-fail threshold = false, want true")
	}
}
