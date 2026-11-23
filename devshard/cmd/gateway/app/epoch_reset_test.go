package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/perf"
)

const fixedClockSeconds = 1_700_000_000

type resetTally struct {
	order          []string
	windowPairs    int
	cutOff         int
	withheld       int
	narratedEpochs []uint64
	narrated       [][3]int
}

func (r *resetTally) ResetAll() (int, int) {
	r.order = append(r.order, "windows")
	return r.windowPairs, r.cutOff
}

func (r *resetTally) ResetHostStats() int {
	r.order = append(r.order, "hosts")
	return r.withheld
}

func (r *resetTally) ResetLookupRefusals() { r.order = append(r.order, "lookups") }

func (r *resetTally) HostStatsReset(epoch uint64, pairs, withheld, cutOff int) {
	r.order = append(r.order, "narrated")
	r.narratedEpochs = append(r.narratedEpochs, epoch)
	r.narrated = append(r.narrated, [3]int{pairs, withheld, cutOff})
}

func newEpochBoundaryReset(tally *resetTally) *epochBoundaryReset {
	return &epochBoundaryReset{
		repriceWindows: func(chain.PhaseSnapshot) { tally.order = append(tally.order, "weights") },
		windows:        tally, hosts: tally, lookups: tally, narrator: tally,
	}
}

func effectiveEpoch(epoch, effective uint64) chain.PhaseSnapshot {
	return chain.PhaseSnapshot{EpochIndex: epoch, EffectiveEpochIndex: effective}
}

func resetsSeen(tally *resetTally) int { return len(tally.narratedEpochs) }

// Test flow:
//  1. Publish a snapshot with effective epoch 463 to set the baseline, then 464 twice and 464 once more.
//  2. Count the resets after each publish.
//  3. Assert the baseline fires nothing, the rise to 464 fires once with the pair counts of both resets, and the repeats fire nothing.
func TestAnEffectiveEpochRiseResetsOnceAndTheFirstSnapshotOnlySetsTheBaseline(t *testing.T) {
	tally := &resetTally{windowPairs: 9, cutOff: 2, withheld: 3}
	reset := newEpochBoundaryReset(tally)

	reset.observe(effectiveEpoch(463, 463))
	require.Zero(t, resetsSeen(tally), "a fresh process is already clean")
	reset.observe(effectiveEpoch(464, 464))
	reset.observe(effectiveEpoch(464, 464))
	reset.observe(effectiveEpoch(464, 464))

	require.Equal(t, []uint64{464}, tally.narratedEpochs)
	require.Equal(t, [][3]int{{9, 3, 2}}, tally.narrated)
}

// Test flow:
//  1. Publish effective epoch 463, then epoch 464 at PoC start with the effective epoch still 463.
//  2. Publish every confirmation-PoC phase with both epochs unchanged.
//  3. Assert nothing was reset, only the weights were refreshed each time.
func TestPoCStartAndConfirmationPoCNeverReset(t *testing.T) {
	tally := &resetTally{}
	reset := newEpochBoundaryReset(tally)
	reset.observe(effectiveEpoch(463, 463))
	reset.observe(effectiveEpoch(464, 463))
	for _, phase := range []chain.ConfirmationPoCPhase{
		chain.ConfirmationPoCGracePeriod, chain.ConfirmationPoCGeneration, chain.ConfirmationPoCValidation, chain.ConfirmationPoCCompleted,
	} {
		snapshot := effectiveEpoch(464, 463)
		snapshot.ConfirmationPoCPhase = phase
		snapshot.RequestsBlocked = true
		snapshot.BlockReason = chain.BlockReasonConfirmationPoC
		reset.observe(snapshot)
	}

	require.Zero(t, resetsSeen(tally))
	for _, step := range tally.order {
		require.Equal(t, "weights", step, "only the weights refresh when no epoch boundary was crossed")
	}
}

// Test flow:
//  1. Publish effective epoch 463, then a snapshot whose effective epoch is missing (zero), then 464, then 464 again.
//  2. Count the resets.
//  3. Assert the zero fires nothing and the rise from 463 to 464 afterwards fires exactly once.
func TestAMissingEpochFollowedByTheRealOneFiresOnce(t *testing.T) {
	tally := &resetTally{}
	reset := newEpochBoundaryReset(tally)

	reset.observe(effectiveEpoch(463, 463))
	reset.observe(effectiveEpoch(463, chain.EpochIndexUnknown))
	reset.observe(effectiveEpoch(464, 464))
	reset.observe(effectiveEpoch(464, 464))

	require.Equal(t, []uint64{464}, tally.narratedEpochs)
}

// Test flow:
//  1. Publish a zero effective epoch first, then 464, then 465.
//  2. Read the order of calls on the 465 publish.
//  3. Assert the zero and the first real epoch set no reset, and at 465 the weights refresh first, then the windows, the hosts and the lookups reset, and the line is written last.
func TestTheWeightsAreRefreshedBeforeTheResetReopensTheWindows(t *testing.T) {
	tally := &resetTally{}
	reset := newEpochBoundaryReset(tally)
	reset.observe(effectiveEpoch(0, chain.EpochIndexUnknown))
	reset.observe(effectiveEpoch(464, 464))
	tally.order = nil

	reset.observe(effectiveEpoch(465, 465))

	require.Equal(t, []string{"weights", "windows", "hosts", "lookups", "narrated"}, tally.order)
}

// Test flow:
//  1. Build a real limiter, refresh it from an epoch-463 snapshot where the host holds weight 10,000, take a lease and let an upstream fault narrow its window.
//  2. Publish an epoch-464 snapshot where the host holds 40,000 through the same subscriber.
//  3. Assert the output window opened wider than it was narrowed to, equal to what a limiter shown only the 464 weights opens at, and the earlier lease's in-flight tokens were kept until its release.
func TestTheWindowsReopenAtTheNewEpochsWeights(t *testing.T) {
	fixedClock := func() time.Time { return time.Unix(fixedClockSeconds, 0) }
	const model, host = "model-a", "participant-a"
	settings := config.Defaults()
	limiter := limits.NewParticipantLimiter(limits.ParticipantConfigFromConfig(&settings), fixedClock)
	hosts := perf.NewTracker(config.NewHolder(&settings), fixedClock)
	refresh := func(snapshot chain.PhaseSnapshot) {
		limiter.ObserveModels(contextWindowsOf(snapshot))
		limiter.ObserveWeights(participantWeightsOf(snapshot))
	}
	reset := &epochBoundaryReset{repriceWindows: refresh, windows: limiter, hosts: hosts, lookups: &resetTally{}, narrator: &resetTally{}}
	snapshotAt := func(epoch uint64, weight float64) chain.PhaseSnapshot {
		return chain.PhaseSnapshot{
			EffectiveEpochIndex:   epoch,
			Models:                map[string]chain.ModelParams{model: {ContextWindow: 32_768}},
			FullWeightsByModel:    map[string]map[string]float64{model: {host: weight}},
			CurrentWeightsByModel: map[string]map[string]float64{model: {host: weight}},
		}
	}
	reset.observe(snapshotAt(463, 10_000))
	release, admission := limiter.Acquire(host, model, limits.TokenCost{Input: 100, Output: 50})
	require.Equal(t, limits.AdmissionOpen, admission)
	limiter.OnResult(limits.Result{Participant: host, Model: model, Verdict: limits.UpstreamFault})
	narrowed, _ := limiter.WindowFor(host, model)

	reset.observe(snapshotAt(464, 40_000))

	reopened, _ := limiter.WindowFor(host, model)
	fresh := limits.NewParticipantLimiter(limits.ParticipantConfigFromConfig(&settings), fixedClock)
	fresh.ObserveModels(contextWindowsOf(snapshotAt(464, 40_000)))
	fresh.ObserveWeights(participantWeightsOf(snapshotAt(464, 40_000)))
	fresh.Acquire(host, model, limits.TokenCost{})
	want, _ := fresh.WindowFor(host, model)
	require.Greater(t, reopened.OutputWindowTokens, narrowed.OutputWindowTokens)
	require.Equal(t, want.InputWindowTokens, reopened.InputWindowTokens)
	require.Equal(t, want.OutputWindowTokens, reopened.OutputWindowTokens)
	require.Equal(t, int64(50), reopened.InflightOutputTokens)
	release()
	released, _ := limiter.WindowFor(host, model)
	require.Zero(t, released.InflightOutputTokens)
}
