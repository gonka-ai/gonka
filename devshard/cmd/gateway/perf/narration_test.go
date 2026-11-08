package perf

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type recordedReturn struct {
	participant string
	model       string
}

type recordingHostNarrator struct {
	withheld            []Withholding
	returned            []recordedReturn
	contextLimits       [][2]uint64
	toolsUnsupported    []string
	versionsUnsupported []string
}

func (n *recordingHostNarrator) HostWithheld(withheld Withholding) {
	n.withheld = append(n.withheld, withheld)
}

func (n *recordingHostNarrator) HostReturned(participant, model string, _ int) {
	n.returned = append(n.returned, recordedReturn{participant: participant, model: model})
}

func (n *recordingHostNarrator) HostContextLimit(_, _ string, contextLimit, previousContextLimit uint64) {
	n.contextLimits = append(n.contextLimits, [2]uint64{contextLimit, previousContextLimit})
}

func (n *recordingHostNarrator) HostToolsUnsupported(participant, model string) {
	n.toolsUnsupported = append(n.toolsUnsupported, participant+"/"+model)
}

func (n *recordingHostNarrator) HostVersionUnsupported(participant string) {
	n.versionsUnsupported = append(n.versionsUnsupported, participant)
}

// Test flow:
//  1. Build a tracker with a no-cap perf config and a recording narrator attached.
//  2. Drive participant-a/model-a to consecutive failures past the ejection threshold.
//  3. Assert exactly one withholding was narrated, with reason "consecutive_failures", ejection count 1, the threshold's consecutive-failure count, and a 30-second withheld-for duration.
func TestEjectionIsNarratedWhenItStarts(t *testing.T) {
	perf := noCap(testPerf())
	tracker := newTestTracker(perf, fixedNow(testEpoch))
	narrator := &recordingHostNarrator{}
	tracker.SetNarrator(narrator)

	failAllConsecutive(tracker, "participant-a", "model-a", perf.ConsecutiveFailThreshold)

	require.Len(t, narrator.withheld, 1, "an ejection nobody hears about cannot be explained afterwards")
	withheld := narrator.withheld[0]
	require.Equal(t, "participant-a", withheld.Participant)
	require.Equal(t, "model-a", withheld.Model)
	require.Equal(t, "consecutive_failures", withheld.Reason)
	require.Equal(t, 1, withheld.EjectionCount)
	require.Equal(t, int(perf.ConsecutiveFailThreshold), withheld.ConsecutiveFailures)
	require.Equal(t, 30*time.Second, withheld.WithheldFor)
}

// Test flow:
//  1. Build a tracker with a no-cap perf config and a recording narrator attached.
//  2. Record failure, failure, success, failure, success for participant-a/model-a, so the failure rate trips at the minimum volume with no consecutive-failure run.
//  3. Assert exactly one withholding was narrated, with reason "failure_rate", the rate 0.6 and the volume 5 that tripped it, and no consecutive failures.
func TestFailureRateEjectionIsNarratedWithTheRateThatTrippedIt(t *testing.T) {
	perf := noCap(testPerf())
	tracker := newTestTracker(perf, fixedNow(testEpoch))
	narrator := &recordingHostNarrator{}
	tracker.SetNarrator(narrator)

	for _, responsive := range []bool{false, false, true, false, true} {
		tracker.RecordSample(Sample{ParticipantKey: "participant-a", Model: "model-a", Responsive: responsive})
	}

	require.Len(t, narrator.withheld, 1)
	withheld := narrator.withheld[0]
	require.Equal(t, "failure_rate", withheld.Reason)
	require.InDelta(t, 0.6, withheld.FailureRate, 1e-9)
	require.InDelta(t, 5.0, withheld.FailureVolume, 1e-9)
	require.Equal(t, 0, withheld.ConsecutiveFailures)
}

// Test flow:
//  1. Build a tracker and eject participant-a/model-a via consecutive failures before attaching a recording narrator.
//  2. Advance the clock past the ejection window and record one responsive sample.
//  3. Assert the narrator recorded exactly one return for participant-a/model-a.
func TestReturnToRoutingIsNarrated(t *testing.T) {
	perf := noCap(testPerf())
	instant := testEpoch
	tracker := newTestTracker(perf, func() time.Time { return instant })
	failAllConsecutive(tracker, "participant-a", "model-a", perf.ConsecutiveFailThreshold)
	narrator := &recordingHostNarrator{}
	tracker.SetNarrator(narrator)

	instant = instant.Add(time.Duration(perf.EjectionBaseSeconds)*time.Second + time.Second)
	tracker.RecordSample(Sample{ParticipantKey: "participant-a", Model: "model-a", Responsive: true})

	require.Equal(t, []recordedReturn{{participant: "participant-a", model: "model-a"}}, narrator.returned)
}

// Test flow:
//  1. Build a tracker and eject participant-a/model-a via consecutive failures, then attach a recording narrator.
//  2. Advance the clock past the ejection window and record one failure, which finds the ejection lapsed and trips it again.
//  3. Assert the renewal was narrated as a withholding at ejection count 2 for the second rung's 60 seconds, and no return was narrated.
func TestAnEjectionRenewedByTheSampleThatFindsItLapsedIsNarrated(t *testing.T) {
	perf := noCap(testPerf())
	instant := testEpoch
	tracker := newTestTracker(perf, func() time.Time { return instant })
	failAllConsecutive(tracker, "participant-a", "model-a", perf.ConsecutiveFailThreshold)
	narrator := &recordingHostNarrator{}
	tracker.SetNarrator(narrator)

	instant = instant.Add(time.Duration(perf.EjectionBaseSeconds)*time.Second + time.Second)
	tracker.RecordSample(Sample{ParticipantKey: "participant-a", Model: "model-a", Responsive: false})

	require.Len(t, narrator.withheld, 1)
	require.Equal(t, 2, narrator.withheld[0].EjectionCount)
	require.Equal(t, 60*time.Second, narrator.withheld[0].WithheldFor)
	require.Empty(t, narrator.returned)
}

// Test flow:
//  1. Build a tracker with a recording narrator attached.
//  2. Drive participant-a/model-a to one failure short of the ejection threshold.
//  3. Assert nothing was narrated as withheld.
func TestASampleThatChangesNothingIsNotNarrated(t *testing.T) {
	perf := noCap(testPerf())
	tracker := newTestTracker(perf, fixedNow(testEpoch))
	narrator := &recordingHostNarrator{}
	tracker.SetNarrator(narrator)

	failAllConsecutive(tracker, "participant-a", "model-a", perf.ConsecutiveFailThreshold-1)

	require.Empty(t, narrator.withheld)
}

// Test flow:
//  1. Build a tracker with a recording narrator attached.
//  2. Record context limits 8192, then 16384, then 4096 for participant-a/model-a.
//  3. Record two tool-unsupported refusals and two version-unsupported refusals for participant-a.
//  4. Assert only the tightening context-limit transitions were narrated ({8192,0} and {4096,8192}), along with a single tools-unsupported entry and a single version-unsupported entry.
func TestACapabilityRefusalIsNarratedOnceAndOnlyWhenItTightens(t *testing.T) {
	tracker := newTestTracker(testPerf(), fixedNow(testEpoch))
	narrator := &recordingHostNarrator{}
	tracker.SetNarrator(narrator)

	tracker.RecordContextLimit("participant-a", "model-a", 8192)
	tracker.RecordContextLimit("participant-a", "model-a", 16384)
	tracker.RecordContextLimit("participant-a", "model-a", 4096)
	tracker.RecordToolUnsupported("participant-a", "model-a")
	tracker.RecordToolUnsupported("participant-a", "model-a")
	tracker.RecordVersionUnsupported("participant-a")
	tracker.RecordVersionUnsupported("participant-a")

	require.Equal(t, [][2]uint64{{8192, 0}, {4096, 8192}}, narrator.contextLimits)
	require.Equal(t, []string{"participant-a/model-a"}, narrator.toolsUnsupported)
	require.Equal(t, []string{"participant-a"}, narrator.versionsUnsupported)
}

// Test flow:
//  1. Build a tracker with no narrator attached.
//  2. Drive participant-a/model-a to consecutive failures past the ejection threshold.
//  3. Assert the tracker still reports the participant/model pair as ejected.
func TestAnUnnarratedTrackerStillWithholds(t *testing.T) {
	perf := noCap(testPerf())
	tracker := newTestTracker(perf, fixedNow(testEpoch))

	failAllConsecutive(tracker, "participant-a", "model-a", perf.ConsecutiveFailThreshold)

	require.True(t, tracker.Ejected("participant-a", "model-a"))
}

// Test flow:
//  1. Build a tracker with a no-cap perf config and a recording narrator attached.
//  2. Record five failures for participant-a/model-a with the consecutive-failure threshold raised to five, so the run and the failure rate both trip on the fifth.
//  3. Assert exactly one withholding was narrated, with reason "consecutive_failures", a run of five, and the rate 1 over the volume 5 that tripped with it.
func TestEjectionTrippedByBothTriggersIsNarratedAsConsecutiveFailures(t *testing.T) {
	perf := noCap(testPerf())
	perf.ConsecutiveFailThreshold = 5
	tracker := newTestTracker(perf, fixedNow(testEpoch))
	narrator := &recordingHostNarrator{}
	tracker.SetNarrator(narrator)

	failAllConsecutive(tracker, "participant-a", "model-a", perf.ConsecutiveFailThreshold)

	require.Len(t, narrator.withheld, 1)
	withheld := narrator.withheld[0]
	require.Equal(t, "consecutive_failures", withheld.Reason)
	require.Equal(t, 5, withheld.ConsecutiveFailures)
	require.InDelta(t, 1.0, withheld.FailureRate, 1e-9)
	require.InDelta(t, 5.0, withheld.FailureVolume, 1e-9)
}
