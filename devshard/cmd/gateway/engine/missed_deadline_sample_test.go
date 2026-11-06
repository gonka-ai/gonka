package engine

import (
	"testing"
	"time"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/perf"
)

// Test flow:
//  1. Build a successful race with a silent cancelled loser that missed its first-token deadline.
//  2. Vary the deadline, progress, winner, client, phase and local-fault facts.
//  3. Record only a proven silent loser as an unresponsive sample and keep the other exemptions.
func TestACancelledSilentLoserKeepsItsDeadlineFailure(t *testing.T) {
	base := AttemptOutcome{
		Participant: "silent", Nonce: 1, SendTime: testEpoch,
		ReceiptTime: testEpoch.Add(10 * time.Millisecond),
		Completed:   testEpoch.Add(35 * time.Second),
		Terminal:    TerminalClientCancelled, FirstTokenDeadlineMissed: true,
	}
	tests := []struct {
		name     string
		change   func(*RaceOutcome, *AttemptOutcome)
		recorded bool
	}{
		{"first-token deadline", func(*RaceOutcome, *AttemptOutcome) {}, true},
		{"receipt deadline", func(_ *RaceOutcome, a *AttemptOutcome) {
			a.FirstTokenDeadlineMissed, a.ReceiptDeadlineMissed = false, true
			a.ReceiptTime = time.Time{}
		}, true},
		{"no missed deadline", func(_ *RaceOutcome, a *AttemptOutcome) { a.FirstTokenDeadlineMissed = false }, false},
		{"eventually produced a token", func(_ *RaceOutcome, a *AttemptOutcome) { a.FirstToken = testEpoch.Add(7 * time.Second) }, false},
		{"finished nonce", func(_ *RaceOutcome, a *AttemptOutcome) { a.NonceFinished = true }, false},
		{"winner", func(o *RaceOutcome, a *AttemptOutcome) { o.WinnerNonce = a.Nonce }, false},
		{"failed race", func(o *RaceOutcome, _ *AttemptOutcome) { o.Succeeded = false }, false},
		{"client left", func(o *RaceOutcome, _ *AttemptOutcome) { o.Lifecycle.ClientGone = true }, false},
		{"PoC bypass", func(o *RaceOutcome, _ *AttemptOutcome) { o.PoCBypassActive = true }, false},
		{"phase aborted", func(_ *RaceOutcome, a *AttemptOutcome) { a.PhaseTransitionAborted = true }, false},
		{"state divergent", func(_ *RaceOutcome, a *AttemptOutcome) { a.StateDivergent = true }, false},
		{"local fault", func(_ *RaceOutcome, a *AttemptOutcome) { a.LocalFault = true }, false},
		{"gateway pressure", func(_ *RaceOutcome, a *AttemptOutcome) { a.GatewayPressure = true }, false},
		{"never dispatched", func(_ *RaceOutcome, a *AttemptOutcome) { a.SendTime = time.Time{} }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := base
			o := RaceOutcome{Model: testModel, WinnerNonce: 2, Succeeded: true}
			tc.change(&o, &a)
			sample, exemption := o.Sample(a)
			if got := exemption == SampleRecorded; got != tc.recorded {
				t.Fatalf("sample recorded = %v, want %v; exemption = %v", got, tc.recorded, exemption)
			}
			if tc.recorded && (sample.Responsive || sample.ParticipantKey != "silent" || sample.Model != testModel) {
				t.Fatalf("sample = %+v, want the silent host's unresponsive sample", sample)
			}
		})
	}
}

// Test flow:
//  1. Register five healthy hosts with the production perf thresholds and ejection cap.
//  2. Report five successful races whose silent loser missed its deadline before cancellation.
//  3. Assert the loser becomes degraded and ejected for this model, so later races stop treating it as healthy.
func TestSilentCancelledLosersReachTheHealthDetector(t *testing.T) {
	cfg := config.Defaults()
	tracker := perf.NewTracker(config.NewHolder(&cfg), func() time.Time { return testEpoch })
	for _, participant := range []string{"silent", "healthy-1", "healthy-2", "healthy-3", "healthy-4"} {
		tracker.RecordSample(perf.Sample{ParticipantKey: participant, Model: testModel, Responsive: true})
	}
	a := AttemptOutcome{
		Participant: "silent", Nonce: 1, SendTime: testEpoch,
		ReceiptTime: testEpoch.Add(10 * time.Millisecond),
		Completed:   testEpoch.Add(35 * time.Second),
		Terminal:    TerminalClientCancelled, FirstTokenDeadlineMissed: true,
	}
	o := RaceOutcome{Model: testModel, WinnerNonce: 2, Succeeded: true}
	for range cfg.Perf.ConsecutiveFailThreshold {
		sample, exemption := o.Sample(a)
		if exemption == SampleRecorded {
			tracker.RecordSample(sample)
		}
	}
	if !tracker.Degraded("silent", testModel) || !tracker.Ejected("silent", testModel) {
		t.Fatal("silent loser stayed healthy after five proven deadline failures")
	}
	if tracker.Degraded("silent", "other-model") || tracker.Ejected("healthy-1", testModel) {
		t.Fatal("deadline failures affected another host or model")
	}
}

// Test flow:
//  1. Start a two-host race whose primary sends a receipt and then nothing, with a 6 s first-token floor and a 30 s loser grace.
//  2. Move the clock past the first-token deadline so the race judges the miss and dispatches the rescue host, which wins.
//  3. Move the clock past the loser grace so the race cancels the silent primary and reports.
//  4. Assert the primary is reported as a cancelled loser that missed its first-token deadline and its host got one unresponsive sample.
func TestARaceReportsItsSilentCancelledLoserToTheTracker(t *testing.T) {
	policy := EscalationPolicy{
		ReceiptTimeout:        time.Hour,
		FirstTokenFloor:       6 * time.Second,
		InterChunkStall:       time.Hour,
		LoserGrace:            30 * time.Second,
		MaxAttemptsPerRequest: 2,
	}
	sim := newSimulator(t, policy, 2, qwenModel)
	sim.host(silentHostNonce, 0, "silent-host", &hostScript{receipt: true, hold: true})
	rescued := rescueHost(sim)

	outcomes := runInBackground(t, sim)
	sim.clock.waitArmed(t, policy.FirstTokenFloor)
	sim.clock.advance(policy.FirstTokenFloor)
	waitForValue(t, rescued, "a dispatch to the rescue host")
	waitForValue(t, outcomes, "the race's answer")
	sim.clock.waitArmed(t, policy.LoserGrace)
	sim.clock.advance(policy.LoserGrace)

	reported := sim.reported(t)
	silent := attemptFor(t, reported, silentHostNonce)
	if silent.Terminal != TerminalClientCancelled || !silent.FirstTokenDeadlineMissed || reported.WinnerNonce != rescueHostNonce {
		t.Fatalf("silent attempt = %v missed first token %v, winner %d; want a cancelled loser past its deadline beside the rescue winner",
			silent.Terminal, silent.FirstTokenDeadlineMissed, reported.WinnerNonce)
	}
	var silentSamples []perf.Sample
	for _, sample := range sim.perf.recorded() {
		if sample.ParticipantKey == "silent-host" {
			silentSamples = append(silentSamples, sample)
		}
	}
	if len(silentSamples) != 1 || silentSamples[0].Responsive {
		t.Fatalf("silent-host samples = %+v, want one unresponsive sample", silentSamples)
	}
	sim.settleAll()
}
