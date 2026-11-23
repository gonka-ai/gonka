package scheduler

import (
	"testing"
	"time"

	"devshard/cmd/gateway/config"
)

const otherModel = "model-other"

// Test flow:
//  1. Build a scheduler whose network config sets every nonce-holding key and whose second model overrides them all.
//  2. Read the match wait, the forced-send run and the per-request burn cap, and the burn limits of a pick, for the first model and for the second one.
//  3. Assert the first model reads the network values and the second its own, in the getters the dispatchers and the pick share.
func TestTheNonceHoldingKeysAreReadPerModel(t *testing.T) {
	t.Parallel()
	settings := config.Defaults()
	settings.Scheduler.MatchWaitMS, settings.Scheduler.MaxConsecutiveBurns, settings.Scheduler.MaxBurnsPerRequest = 100, 2, 16
	wait, run, perRequest, attempts := int64(900), int64(5), int64(3), int64(1)
	if err := settings.ResolveModels(map[string]config.ModelSettings{otherModel: {MatchWaitMS: &wait, MaxConsecutiveBurns: &run, MaxBurnsPerRequest: &perRequest, EngineMaxAttemptsPerRequest: &attempts}}); err != nil {
		t.Fatalf("ResolveModels() = %v", err)
	}
	probe := &Scheduler{settings: config.NewHolder(&settings)}

	if got := probe.matchWait(modelA); got != 100*time.Millisecond {
		t.Errorf("matchWait(modelA) = %v, want the network 100ms", got)
	}
	if got := probe.matchWait(otherModel); got != 900*time.Millisecond {
		t.Errorf("matchWait(otherModel) = %v, want its own 900ms", got)
	}
	if got := probe.burnLimits(modelA); got.consecutive != 2 || got.perRequest != 16 {
		t.Errorf("burnLimits(modelA) = %+v, want the network 2 and 16", got)
	}
	if got := probe.burnLimits(otherModel); got.consecutive != 5 || got.perRequest != 3 {
		t.Errorf("burnLimits(otherModel) = %+v, want its own 5 and 3", got)
	}
	if got := probe.attemptsToFund(otherModel); got != 1 {
		t.Errorf("attemptsToFund(otherModel) = %d, want its own 1", got)
	}
	if got := probe.attemptsToFund(modelA); got != int(config.Defaults().Engine.MaxAttemptsPerRequest) {
		t.Errorf("attemptsToFund(modelA) = %d, want the network default", got)
	}
}
