package scheduler

import (
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/perf"
)

const resetModel = "model-reset"

type resetFleet struct {
	scheduler *Scheduler
	limiter   *limits.ParticipantLimiter
	tracker   *perf.Tracker
}

func newResetFleet(shadowed ...string) resetFleet {
	settings := config.Defaults()
	settings.Scheduler.Participants.Shadow = shadowed
	holder := config.NewHolder(&settings)
	now := func() time.Time { return time.Unix(0, 0) }
	limiter := limits.NewParticipantLimiter(limits.ParticipantConfigFromConfig(&settings), now)
	tracker := perf.NewTracker(holder, now)
	return resetFleet{
		scheduler: &Scheduler{limiter: limiter, perf: tracker, settings: holder},
		limiter:   limiter,
		tracker:   tracker,
	}
}

func (f resetFleet) wreck(participant string) {
	for range int(config.Defaults().Perf.ConsecutiveFailThreshold) {
		f.tracker.RecordSample(perf.Sample{ParticipantKey: participant, Model: resetModel})
	}
	for range int(config.Defaults().Limits.HostCutoff.AfterFailures) {
		f.limiter.OnResult(limits.Result{Participant: participant, Model: resetModel, Verdict: limits.TransportFault})
	}
}

func (f resetFleet) matched(participant string) Decision {
	gates := f.scheduler.fleetGates(resetModel, chain.PhaseSnapshot{}).forEscrow(always(false), always(false), nil)
	return match(HostBinding{Participant: participant}, nil, []string{participant}, gates, time.Time{}, 0)
}

// Test flow:
//  1. Build real limiter and tracker with `hostB` shadowed and drive both `hostA` and `hostB` into an ejection and a cut-off.
//  2. Reset the limiter and the tracker as the epoch boundary does.
//  3. Assert the shadowed slot is a probe before and after the reset, and that the unshadowed one is a burn before the reset and a send after it.
func TestAShadowSlotStaysAProbeAcrossAnEpochBoundaryReset(t *testing.T) {
	fleet := newResetFleet(hostB)
	fleet.wreck(hostA)
	fleet.wreck(hostB)
	if _, probing := fleet.matched(hostB).(probe); !probing {
		t.Fatalf("shadowed slot before the reset = %T, want probe", fleet.matched(hostB))
	}
	if _, burning := fleet.matched(hostA).(burn); !burning {
		t.Fatalf("wrecked unshadowed slot before the reset = %T, want burn", fleet.matched(hostA))
	}

	fleet.limiter.ResetAll()
	fleet.tracker.ResetHostStats()

	if _, probing := fleet.matched(hostB).(probe); !probing {
		t.Fatalf("shadowed slot after the reset = %T, want probe", fleet.matched(hostB))
	}
	if _, burning := fleet.matched(hostA).(burn); burning {
		t.Fatalf("unshadowed slot after the reset = %T, want it routable again", fleet.matched(hostA))
	}
}
