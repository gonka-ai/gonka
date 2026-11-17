package scheduler

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// groupOf names one host per slot of a group of the given size.
func groupOf(size int) []string {
	group := make([]string, 0, size)
	for index := range size {
		group = append(group, benchParticipant(index))
	}
	return group
}

// busyExcept marks every host but the given one as out of window.
func busyExcept(open string) func(string) bool {
	return func(participant string) bool { return participant != open }
}

// everyHostBut lists every host of the group except the given one.
func everyHostBut(group []string, open string) []string {
	refused := make([]string, 0, len(group))
	for _, participant := range group {
		if participant != open {
			refused = append(refused, participant)
		}
	}
	return refused
}

// Test flow:
//  1. Build a harness with a 16-host group, every host but the first busy, every host refused, and a max of 6 consecutive burns.
//  2. Submit and await two requests in turn.
//  3. Assert the first lands on host 7 and the second on host 14, each after a run of burns.
//  4. Assert 12 burns were recorded, 6 for each send, and each forced send names its 6-burn run.
//  5. Assert the limiter recorded 2 overdrafts, one per forced send crossing the window.
func TestABurnRunBuysOneSendAndThenStartsOver(t *testing.T) {
	group := groupOf(16)
	test := newHarness(t, harnessConfig{
		slots:               group,
		windowFull:          busyExcept(group[0]),
		refused:             group,
		maxConsecutiveBurns: 6,
	})

	first := test.submit(t, test.clock.Now())
	wantAssignment(t, awaitReply(t, first), group[7], 7)

	second := test.submit(t, test.clock.Now())
	wantAssignment(t, awaitReply(t, second), group[14], 14)
	test.dispatcher.stop()

	require.Equal(t, slices.Repeat([]string{GhostReasonWindowFull}, 12), test.observer.burns(),
		"six burns are the price of each of the two sends")
	require.Equal(t, []forcedSend{
		{participant: group[7], burnsInARow: 6},
		{participant: group[14], burnsInARow: 6},
	}, test.observer.forcedSends(), "each send must name the run it ended")
	require.Equal(t, 2, test.limiter.overdrafts(), "a forced send crosses the window rather than asking it")
}

// Test flow:
//  1. Build a harness with a 16-host group, every host but host 3 busy, every host but host 3 refused, and a max of 6 consecutive burns.
//  2. Submit and await a first request that lands on host 3, an ordinary serve, and assert no forced send was recorded for it.
//  3. Submit and await a second request that lands on host 10.
//  4. Assert 8 burns were recorded, two before the ordinary serve and a full run of six after.
//  5. Assert the one forced send names a fresh 6-burn run, reset by the ordinary serve in between.
func TestAnOrdinaryServeStartsTheRunOver(t *testing.T) {
	group := groupOf(16)
	test := newHarness(t, harnessConfig{
		slots:               group,
		windowFull:          busyExcept(group[3]),
		refused:             everyHostBut(group, group[3]),
		maxConsecutiveBurns: 6,
	})

	first := test.submit(t, test.clock.Now())
	wantAssignment(t, awaitReply(t, first), group[3], 3)
	require.Empty(t, test.observer.forcedSends(), "a host with room is an ordinary serve, whatever the run behind it")

	second := test.submit(t, test.clock.Now())
	wantAssignment(t, awaitReply(t, second), group[10], 10)
	test.dispatcher.stop()

	require.Len(t, test.observer.burns(), 8, "two burns before the ordinary serve, then a full run of six")
	require.Equal(t, []forcedSend{{participant: group[10], burnsInARow: 6}}, test.observer.forcedSends(),
		"the second send must be bought by a run that started from zero")
}

// Test flow:
//  1. Build a harness with a 16-host group, every host refused at the acquire, and a max of 3 consecutive burns.
//  2. Submit and await a costly request.
//  3. Assert it lands on host 4 after 3 burns, and the forced send names that 3-burn run.
//  4. Assert the limiter recorded 1 overdraft.
//  5. Assert the last charged cost matches the request's own cost, not the window it crossed.
func TestTheRungAlsoCoversARefusalThePeekDidNotPredict(t *testing.T) {
	group := groupOf(16)
	test := newHarness(t, harnessConfig{
		slots:               group,
		refused:             group,
		maxConsecutiveBurns: 3,
	})
	costly := RequestProfile{Model: modelA, InputTokens: 9_000, OutputTokens: 120, Params: "payload"}

	queued := test.submitCosting(t, costly, test.clock.Now())
	wantAssignment(t, awaitReply(t, queued), group[4], 4)
	test.dispatcher.stop()

	require.Len(t, test.observer.burns(), 3, "three burns are the whole price of this send")
	require.Equal(t, []forcedSend{{participant: group[4], burnsInARow: 3}}, test.observer.forcedSends())
	require.Equal(t, 1, test.limiter.overdrafts())
	require.Equal(t, slotCost(costly), test.limiter.chargedCosts()[len(test.limiter.chargedCosts())-1],
		"the tokens a forced send spends are the request's own, or the window it crossed means nothing")
}

// Test flow:
//  1. Build a harness with a 16-host group, every host but the first cut off, and a max of 6 consecutive burns.
//  2. Submit and await a request.
//  3. Assert it lands on host 0 after 15 burns, all of them cut-off ghosts, as if the rung did not exist.
//  4. Assert no forced send was recorded and the limiter recorded no overdraft.
func TestAForcedSendNeverCrossesACutOff(t *testing.T) {
	group := groupOf(16)
	test := newHarness(t, harnessConfig{
		slots:               group,
		cutOff:              busyExcept(group[0]),
		maxConsecutiveBurns: 6,
	})

	queued := test.submit(t, test.clock.Now())
	wantAssignment(t, awaitReply(t, queued), group[0], 16)
	test.dispatcher.stop()

	require.Equal(t, slices.Repeat([]string{GhostReasonCutOff}, 15), test.observer.burns(),
		"a cut-off host burns its nonce as it did before the rung existed")
	require.Empty(t, test.observer.forcedSends(), "no send may be forced onto a host the cut-off holds")
	require.Zero(t, test.limiter.overdrafts())
}

// Test flow:
//  1. Build a harness with a 16-host group, every host but host 0 busy and refused, and the rung off (no max set).
//  2. Submit and await a request with the rung off, land on host 0 after 15 burns, and assert no forced send was recorded.
//  3. Set the burn limit to 3.
//  4. Submit and await a second request.
//  5. Assert it lands on host 4 and the forced send names a fresh 3-burn run, the limit picked up mid-drain.
func TestTheRunLengthIsReadAfreshOnEveryDrain(t *testing.T) {
	group := groupOf(16)
	test := newHarness(t, harnessConfig{
		slots:      group,
		windowFull: busyExcept(group[0]),
		refused:    everyHostBut(group, group[0]),
	})

	offTheRung := test.submit(t, test.clock.Now())
	wantAssignment(t, awaitReply(t, offTheRung), group[0], 16)
	require.Equal(t, slices.Repeat([]string{GhostReasonWindowFull}, 15), test.observer.burns(),
		"with the rung off the drain burns until the nonce reaches a host with room")
	require.Empty(t, test.observer.forcedSends())

	test.burnLimit.Store(3)

	onTheRung := test.submit(t, test.clock.Now())
	wantAssignment(t, awaitReply(t, onTheRung), group[4], 20)
	test.dispatcher.stop()

	require.Equal(t, []forcedSend{{participant: group[4], burnsInARow: 3}}, test.observer.forcedSends(),
		"the drain must pick up a run length set after it started")
}

// Test flow:
//  1. Build a harness with two hosts, `hostA` and `hostB`, `hostA` busy and a max of 1 consecutive burn.
//  2. Submit a stale request bound to `hostA` and await its reply.
//  3. Assert it lands on `hostA` after crossing the full window.
//  4. Assert only that one burn was recorded, and `hostA` is recorded as an excluded serve.
//  5. Assert no forced send was recorded, since `hostA` had room once the rescue served it.
func TestAForcedBindingStillRescuesAWaiterThatExcludedIt(t *testing.T) {
	test := newHarness(t, harnessConfig{
		slots:               []string{hostA, hostB},
		windowFull:          busyExcept(hostA),
		maxConsecutiveBurns: 1,
	})

	queued := test.submit(t, test.clock.Now().Add(-2*matchWaitWindow), hostA)
	wantAssignment(t, awaitReply(t, queued), hostA, 2)
	test.dispatcher.stop()

	require.Equal(t, []string{GhostReasonWindowFull}, test.observer.burns(),
		"only the binding on the full host burns; the next one serves rather than burning ghostExclude")
	require.Equal(t, []string{hostA}, test.observer.excludedServes())
	require.Empty(t, test.observer.forcedSends(), "the host had room, so nothing was crossed")
}
