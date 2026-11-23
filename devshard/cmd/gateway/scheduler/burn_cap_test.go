package scheduler

import (
	"cmp"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/env"
)

const burnCapGroupSize = 8

const firstBurnSlot = 1

func burnedSlots(group []string, count int) func(string) bool {
	return func(participant string) bool {
		index := slices.Index(group, participant)
		return index >= firstBurnSlot && index < firstBurnSlot+count
	}
}

// Test flow:
//  1. For each state a slot can be burned in (proof-of-compute, full window, cut-off, ejected, diverged, excluded by the request) build an eight-host group whose first three slots from the cursor are in that state.
//  2. Price the escrow with `expectedBurns`, then submit the same request to a dispatcher over the same predicates and await its serve.
//  3. Assert the estimate equals the burns the drain recorded, three for every burning state.
//  4. Assert the request was served by the first host past the three slots.
//  5. Repeat for a full window under a forced send after two burns and assert the estimate stops at the slot the drain forces a send over.
func TestTheEstimateEqualsTheBurnsTheDrainMakesForEachState(t *testing.T) {
	group := groupOf(burnCapGroupSize)
	states := []struct {
		name       string
		burned     int
		servedAt   int
		forceAfter int64
		config     func(blocked func(string) bool) harnessConfig
		exclude    func() []string
	}{
		{name: "proof-of-compute", burned: 3, config: func(blocked func(string) bool) harnessConfig { return harnessConfig{pocRequired: blocked} }},
		{name: "full window", burned: 3, config: func(blocked func(string) bool) harnessConfig { return harnessConfig{windowFull: blocked} }},
		{name: "cut-off", burned: 3, config: func(blocked func(string) bool) harnessConfig { return harnessConfig{cutOff: blocked} }},
		{name: "ejected", burned: 3, config: func(blocked func(string) bool) harnessConfig { return harnessConfig{ejected: blocked} }},
		{name: "diverged", burned: 3, config: func(blocked func(string) bool) harnessConfig { return harnessConfig{stateBlocked: blocked} }},
		{name: "excluded by the request", burned: 3, config: func(blocked func(string) bool) harnessConfig { return harnessConfig{} }, exclude: func() []string { return group[firstBurnSlot : firstBurnSlot+3] }},
		{name: "full window under the forced send", burned: 2, servedAt: firstBurnSlot + 2, forceAfter: pickCapForcedAfter, config: func(blocked func(string) bool) harnessConfig {
			return harnessConfig{windowFull: blocked, maxConsecutiveBurns: pickCapForcedAfter}
		}},
	}

	for _, state := range states {
		t.Run(state.name, func(t *testing.T) {
			config := state.config(burnedSlots(group, 3))
			config.slots = group
			test := newHarness(t, config)
			var excluded []string
			if state.exclude != nil {
				excluded = state.exclude()
			}
			profile := RequestProfile{Model: modelA, Exclude: excluded, Params: "payload"}
			estimate := expectedBurns(Escrow{Session: test.session}, config.predicates(chain.PhaseSnapshot{}), newWaiter(profile, test.clock.Now()), 0, state.forceAfter).burns

			queued := test.submitCosting(t, profile, test.clock.Now().Add(-2*matchWaitWindow))
			servedAt := cmp.Or(state.servedAt, firstBurnSlot+3)
			wantAssignment(t, awaitReply(t, queued), group[servedAt], uint64(servedAt))
			test.dispatcher.stop()

			require.Equal(t, state.burned, estimate, "the estimate")
			require.Len(t, test.observer.burns(), estimate, "the burns the drain made")
		})
	}
}

// Test flow:
//  1. Build an eight-host group whose first five slots from the cursor are ejected, with a cap of three burns per request.
//  2. Submit a request carrying its own burn tally and await its reply.
//  3. Assert the reply is the overloaded error, exactly three burns were recorded, and no fourth nonce was committed.
//  4. Submit a second request with a tally of its own and assert it burns the two ejected slots left and is served by the next host, since the count belongs to the request.
func TestARequestIsAnsweredOverloadedAfterItsCapOfBurns(t *testing.T) {
	group := groupOf(burnCapGroupSize)
	test := newHarness(t, harnessConfig{slots: group, ejected: burnedSlots(group, 5), maxBurnsPerRequest: 3})

	first := test.submitCosting(t, RequestProfile{Model: modelA, Params: "payload", Burns: &BurnTally{}}, test.clock.Now())
	reply := awaitReply(t, first)
	require.ErrorIs(t, reply.err, ErrRequestCapped)
	require.Len(t, test.observer.burns(), 3)
	_, _, commits := test.session.report()
	require.Len(t, commits, 3, "the declined fourth nonce is not committed")

	second := test.submitCosting(t, RequestProfile{Model: modelA, Params: "payload", Burns: &BurnTally{}}, test.clock.Now())
	wantAssignment(t, awaitReply(t, second), group[firstBurnSlot+5], firstBurnSlot+5)
	test.dispatcher.stop()
	require.Len(t, test.observer.burns(), 5, "the second request burned the two slots left of the group's five")
}

// Test flow:
//  1. Build an eight-host group whose first five slots from the cursor are ejected, with a cap of zero.
//  2. Submit a request carrying its own burn tally and await its reply.
//  3. Assert it is served by the first host past the ejected ones after five burns, since zero turns the cap off.
func TestAZeroCapNeverAnswersARequestOverloaded(t *testing.T) {
	group := groupOf(burnCapGroupSize)
	test := newHarness(t, harnessConfig{slots: group, ejected: burnedSlots(group, 5), maxBurnsPerRequest: 0})

	queued := test.submitCosting(t, RequestProfile{Model: modelA, Params: "payload", Burns: &BurnTally{}}, test.clock.Now())

	wantAssignment(t, awaitReply(t, queued), group[firstBurnSlot+5], firstBurnSlot+5)
	test.dispatcher.stop()
	require.Len(t, test.observer.burns(), 5)
}

// Test flow:
//  1. Build an eight-host group whose first two slots from the cursor are ejected, with a cap of three burns per request.
//  2. Submit two requests that share one burn tally, as two attempts of one request do.
//  3. Assert the first is served after two burns and the second, which needs two more, is answered overloaded after one.
func TestTheBurnsOfEveryAttemptOfARequestShareOneCap(t *testing.T) {
	group := groupOf(burnCapGroupSize)
	test := newHarness(t, harnessConfig{slots: group, ejected: func(participant string) bool {
		return slices.Contains([]string{group[1], group[2], group[4], group[5]}, participant)
	}, maxBurnsPerRequest: 3})
	tally := &BurnTally{}

	first := test.submitCosting(t, RequestProfile{Model: modelA, Params: "payload", Burns: tally}, test.clock.Now())
	wantAssignment(t, awaitReply(t, first), group[3], 3)
	second := test.submitCosting(t, RequestProfile{Model: modelA, Params: "payload", Escalation: true, Burns: tally}, test.clock.Now())
	reply := awaitReply(t, second)
	test.dispatcher.stop()

	require.ErrorIs(t, reply.err, ErrRequestCapped)
	require.Len(t, test.observer.burns(), 3, "two burns for the first attempt and the one that reached the cap for the second")
}

// Test flow:
//  1. Build an eight-host group whose first two slots from the cursor are ejected and whose third is one the request excluded, with a cap of two burns.
//  2. Submit a request that excludes that third host and has waited past the match window.
//  3. Assert the two ejected burns are the request's and the burn on the excluded host is charged to nobody, so the request is served by the next host and never sent to the excluded one.
func TestABurnOnAHostTheRequestExcludedIsChargedToNobody(t *testing.T) {
	group := groupOf(burnCapGroupSize)
	excluded := group[3]
	test := newHarness(t, harnessConfig{slots: group, ejected: burnedSlots(group, 2), maxBurnsPerRequest: 2})
	tally := &BurnTally{}

	queued := test.submitCosting(t, RequestProfile{Model: modelA, Params: "payload", Exclude: []string{excluded}, Burns: tally}, test.clock.Now().Add(-2*matchWaitWindow))
	reply := awaitReply(t, queued)
	test.dispatcher.stop()

	wantAssignment(t, reply, group[4], 4)
	require.Len(t, test.observer.burns(), 3)
	require.Empty(t, test.observer.excludedServes())
	require.True(t, tally.Reached(2))
	require.False(t, tally.Reached(3), "the excluded host's burn is nobody's")
}

// Test flow:
//  1. Build a default harness with a cap of two burns, and submit a head request that excludes `hostB`, the host nonce 1 binds, with a tally already at the cap.
//  2. Await the head's reply and assert it is served by `hostA` at nonce 2, since no request could take nonce 1 and so none was charged for it.
//  3. Assert the head's tally still reads two and the burn names no request.
func TestABurnNoWaiterMatchedIsNotChargedToTheHead(t *testing.T) {
	test := newHarness(t, harnessConfig{maxBurnsPerRequest: 2})
	atCap := chargedTally(2)

	head := test.submitCosting(t, RequestProfile{RequestID: "request-head", Model: modelA, Params: "payload", Exclude: []string{hostB}, Burns: atCap}, test.clock.Now().Add(-2*matchWaitWindow))

	wantAssignment(t, awaitReply(t, head), hostA, 2)
	test.dispatcher.stop()
	require.True(t, atCap.Reached(2))
	require.False(t, atCap.Reached(3))
	require.Equal(t, []string{NoRequestID}, test.observer.burnRequests())
}

// Test flow:
//  1. Build a harness whose `hostB`, the host nonce 1 binds, is ejected, with a cap of one burn, and submit a head request that excludes `hostB` and a second request, each with a tally of its own.
//  2. Assert the head is served by `hostA` at nonce 2 and the second, which nonce 3 on the ejected host was decided for, is answered capped at its next burn.
//  3. Assert the second request's tally reads one and the head's none, and the burn names the second.
func TestABurnOnABlockedHostIsChargedToTheFirstWaiterThatDoesNotExcludeIt(t *testing.T) {
	test := newHarness(t, harnessConfig{holdStart: true, maxBurnsPerRequest: 1, ejected: func(participant string) bool { return participant == hostB }})
	headTally, secondTally := &BurnTally{}, &BurnTally{}
	head := test.submitCosting(t, RequestProfile{RequestID: "request-head", Model: modelA, Params: "payload", Exclude: []string{hostB}, Burns: headTally}, test.clock.Now())
	second := test.submitCosting(t, RequestProfile{RequestID: "request-second", Model: modelA, Params: "payload", Burns: secondTally}, test.clock.Now())

	test.dispatcher.start()

	wantAssignment(t, awaitReply(t, head), hostA, 2)
	require.ErrorIs(t, awaitReply(t, second).err, ErrRequestCapped)
	test.dispatcher.stop()
	require.True(t, secondTally.Reached(1))
	require.False(t, headTally.Reached(1), "the head excluded the host, so the burn on it was never the head's")
	require.Equal(t, []string{"request-second"}, test.observer.burnRequests())
}

// Test flow:
//  1. Build a harness where `hostB` is refused at admission, with a cap of one burn, and submit a head request that excludes `hostB` and a second request, each with a tally of its own.
//  2. Start the dispatcher and assert the head is served by `hostA` at nonce 2 and the second, whose slot was refused at nonce 1 and again at nonce 3, is answered capped.
//  3. Assert the second request's tally reads one, the head's none, and the one burn names the second.
func TestARefusedSlotBurnIsChargedToItsWaiterAndCapsIt(t *testing.T) {
	test := newHarness(t, harnessConfig{holdStart: true, maxBurnsPerRequest: 1, refused: []string{hostB}})
	headTally, secondTally := &BurnTally{}, &BurnTally{}
	head := test.submitCosting(t, RequestProfile{RequestID: "request-head", Model: modelA, Params: "payload", Exclude: []string{hostB}, Burns: headTally}, test.clock.Now())
	second := test.submitCosting(t, RequestProfile{RequestID: "request-second", Model: modelA, Params: "payload", Burns: secondTally}, test.clock.Now())

	test.dispatcher.start()

	wantAssignment(t, awaitReply(t, head), hostA, 2)
	require.ErrorIs(t, awaitReply(t, second).err, ErrRequestCapped)
	test.dispatcher.stop()
	require.True(t, secondTally.Reached(1))
	require.False(t, headTally.Reached(1))
	require.Equal(t, []string{"request-second"}, test.observer.burnRequests())
}

// Test flow:
//  1. Build a harness whose session commits a nonce and then fails preparing it, once, and submit a head request that excludes `hostB`, the host nonce 1 binds, and a second request, each with a tally of its own.
//  2. Assert the second request, the one nonce 1 was served for, fails on the spent nonce.
//  3. Assert its tally reads one, the head's none, and the burn names the second.
func TestANonceSpentOnAFailedPrepareIsChargedToTheRequestItWasServedFor(t *testing.T) {
	test := newHarness(t, harnessConfig{holdStart: true, failAfterSpend: errors.New("database is locked")})
	headTally, secondTally := &BurnTally{}, &BurnTally{}
	head := test.submitCosting(t, RequestProfile{RequestID: "request-head", Model: modelA, Params: "payload", Exclude: []string{hostB}, Burns: headTally}, test.clock.Now())
	second := test.submitCosting(t, RequestProfile{RequestID: "request-second", Model: modelA, Params: "payload", Burns: secondTally}, test.clock.Now())

	test.dispatcher.start()

	require.Error(t, awaitReply(t, second).err)
	wantAssignment(t, awaitReply(t, head), hostA, 2)
	test.dispatcher.stop()
	require.True(t, secondTally.Reached(1))
	require.False(t, headTally.Reached(1))
	require.Equal(t, []string{"request-second"}, test.observer.burnRequests())
}

// Test flow:
//  1. Build a harness whose `hostB` is ejected and submit "request-head", which leaves right after nonce 1 is decided for it, then "request-next", with a cap of two burns and a tally for each.
//  2. Start the dispatcher and await "request-next", served by `hostA` at nonce 2.
//  3. Assert the burn names "request-head", the waiter that charge and cap check were made against, and that its tally holds the burn.
func TestABurnNamesTheWaiterItWasDecidedForEvenIfItLeaves(t *testing.T) {
	var abandonOnce sync.Once
	var head *waiter
	test := newHarness(t, harnessConfig{
		holdStart: true, maxBurnsPerRequest: 2, ejected: func(participant string) bool { return participant == hostB },
		afterDecide: func(HostBinding) { abandonOnce.Do(func() { head.abandoned.Store(true) }) },
	})
	headTally := &BurnTally{}
	head = test.submitCosting(t, RequestProfile{RequestID: "request-head", Model: modelA, Params: "payload", Burns: headTally}, test.clock.Now())
	next := test.submitCosting(t, RequestProfile{RequestID: "request-next", Model: modelA, Params: "payload", Burns: &BurnTally{}}, test.clock.Now())

	test.dispatcher.start()

	wantAssignment(t, awaitReply(t, next), hostA, 2)
	test.dispatcher.stop()
	require.Equal(t, []string{"request-head"}, test.observer.burnRequests())
	require.True(t, headTally.Reached(1))
}

// Test flow:
//  1. Build an eight-host group whose first three slots from the cursor are ejected, with the cap read from the live setting and set to two.
//  2. Submit a request and assert it is answered overloaded after two burns.
//  3. Change the setting to zero and submit another request.
//  4. Assert the next request is served after the three burns, since the cap is read when a drain starts and not when the dispatcher does.
func TestTheNextRequestReadsTheCapAsTheSettingStandsNow(t *testing.T) {
	group := groupOf(burnCapGroupSize)
	test := newHarness(t, harnessConfig{slots: group, ejected: burnedSlots(group, 3), maxBurnsPerRequest: 2})

	capped := test.submitCosting(t, RequestProfile{Model: modelA, Params: "payload", Burns: &BurnTally{}}, test.clock.Now())
	require.ErrorIs(t, awaitReply(t, capped).err, ErrRequestCapped)
	test.burnCap.Store(0)
	uncapped := test.submitCosting(t, RequestProfile{Model: modelA, Params: "payload", Burns: &BurnTally{}}, test.clock.Now())
	reply := awaitReply(t, uncapped)
	test.dispatcher.stop()

	require.NoError(t, reply.err)
	require.Len(t, test.observer.burns(), 3, "two burns for the capped request, and the one slot left before a usable host for the next")
}

// Test flow:
//  1. Build a scheduler harness over one twelve-host escrow whose slots one to four and six to nine are ejected, with the default cap.
//  2. Pick for a request carrying a burn tally and assert it is served after four burns.
//  3. Apply `max_burns_per_request` of two the way the settings API does, by building the configuration from the override and swapping it in.
//  4. Pick for a second request and assert it is answered overloaded at the pick with no further burn, then restore the default and assert a third request is served.
func TestAPickReadsTheBurnCapTheSettingsAPILastApplied(t *testing.T) {
	group := groupOf(12)
	test := newSchedulerHarness(t, schedulerConfig{slots: group})
	for _, index := range []int{1, 2, 3, 4, 6, 7, 8, 9} {
		test.perf.eject(group[index])
	}
	pick := func() (Assignment, error) {
		return test.scheduler.Pick(t.Context(), RequestProfile{Model: modelA, Params: "payload", Burns: &BurnTally{}})
	}
	apply := func(overrides env.Tunables) {
		next, err := config.Build(env.Values{}, config.Overrides{Tunables: overrides})
		require.NoError(t, err)
		test.settings.Swap(next)
	}

	first, err := pick()
	wantHost(t, first, err, escrowA, group[5], 5)
	require.Len(t, test.observer.burns(), 4)

	two := int64(2)
	apply(env.Tunables{MaxBurnsPerRequest: &two})
	_, err = pick()
	require.ErrorIs(t, err, ErrRequestCapped)
	require.Len(t, test.observer.burns(), 4, "the cap answered at the pick, before any nonce")

	apply(env.Tunables{})
	third, err := pick()
	wantHost(t, third, err, escrowA, group[10], 10)
}

// Test flow:
//  1. Build a harness whose session commits a nonce and then fails preparing it, once.
//  2. Submit a request carrying its own burn tally and await its reply.
//  3. Assert the request failed on the spent nonce and the tally reached a cap of one, since a nonce spent on a failed prepare is a burn of that request.
func TestANonceSpentOnAFailedPrepareIsChargedToItsRequest(t *testing.T) {
	test := newHarness(t, harnessConfig{failAfterSpend: errors.New("database is locked")})
	tally := &BurnTally{}

	queued := test.submitCosting(t, RequestProfile{Model: modelA, Params: "payload", Burns: tally}, test.clock.Now())
	reply := awaitReply(t, queued)
	test.dispatcher.stop()

	require.Error(t, reply.err)
	require.True(t, tally.Reached(1), "the spent nonce must be counted")
}

// Test flow:
//  1. Build an eight-host group whose first slot from the cursor is a shadow host whose probe the prober refuses and whose next one is ejected, with a cap of one burn.
//  2. Submit a request and await its reply.
//  3. Assert the shadow host's burned nonce neither counted against the cap nor was answered overloaded, and the request was served after the ejected host's one burn.
func TestABurnOnAShadowHostIsNotCountedAgainstTheCap(t *testing.T) {
	group := groupOf(burnCapGroupSize)
	test := newHarness(t, harnessConfig{
		slots: group, shadowed: burnedSlots(group, 1), prober: &recordingProber{refuse: true}, maxBurnsPerRequest: 1,
		ejected: func(participant string) bool { return participant == group[firstBurnSlot+1] },
	})
	tally := &BurnTally{}

	queued := test.submitCosting(t, RequestProfile{Model: modelA, Params: "payload", Burns: tally}, test.clock.Now())
	reply := awaitReply(t, queued)
	test.dispatcher.stop()

	wantAssignment(t, reply, group[firstBurnSlot+2], firstBurnSlot+2)
	require.Equal(t, []string{GhostReasonUntrusted, GhostReasonEjected}, test.observer.burns())
	require.True(t, tally.Reached(1), "only the ejected host's burn is counted")
	require.False(t, tally.Reached(2))
}

// Test flow:
//  1. Build an eight-host group whose first slot from the cursor is a shadow host and whose next one is ejected, with a cap of one burn.
//  2. Price the escrow with `expectedBurns` over the same gates the drain reads, for a request that excludes nobody.
//  3. Assert the estimate counts both slots as nonces spent but charges the request only the ejected host's burn, so a cap of one still leaves the escrow pickable.
func TestTheForecastDoesNotChargeARequestForAShadowSlot(t *testing.T) {
	group := groupOf(burnCapGroupSize)
	gates := openAvailability()
	gates.shadowed = shadowing(group[firstBurnSlot])
	gates.ejected = func(participant string) bool { return participant == group[firstBurnSlot+1] }
	escrow := Escrow{ID: escrowA, Model: modelA, Session: &fakeSession{slots: group}}

	forecast := expectedBurns(escrow, gates, newWaiter(RequestProfile{Model: modelA}, time.Time{}), 0, 0)

	require.Equal(t, burnForecast{burns: 2, charged: 1, usable: true}, forecast)
}

// Test flow:
//  1. Build an eight-host group whose first slot from the cursor is a shadow host whose escrow state diverged, with a cap of one burn.
//  2. Submit a request carrying a tally and await its reply.
//  3. Assert the shadow host's nonce was burned for the diverged state, the request was served on the next host, and the tally stayed at zero: a shadow host's burn is nobody's.
func TestABurnOnADivergedShadowHostIsNotChargedToARequest(t *testing.T) {
	group := groupOf(burnCapGroupSize)
	test := newHarness(t, harnessConfig{
		slots: group, shadowed: burnedSlots(group, 1), prober: &recordingProber{}, maxBurnsPerRequest: 1,
		stateBlocked: func(participant string) bool { return participant == group[firstBurnSlot] },
	})
	tally := &BurnTally{}

	reply := awaitReply(t, test.submitCosting(t, RequestProfile{Model: modelA, Params: "payload", Burns: tally}, test.clock.Now()))
	test.dispatcher.stop()

	wantAssignment(t, reply, group[firstBurnSlot+1], firstBurnSlot+1)
	require.Equal(t, []string{GhostReasonStateDiverged}, test.observer.burns())
	require.False(t, tally.Reached(1), "a shadow host's burn is charged to no request")
}

// Test flow:
//  1. Build a two-host group whose second host is ejected, with a cap of two burns, and a queue of a head request with a fresh tally and a second request whose tally is at the cap.
//  2. Abandon the head right after the burn on the ejected host is decided, then start the dispatcher.
//  3. Await the second request's reply and assert it is served by the first host at nonce 2.
//  4. Assert the second request's tally still reads two, since the burn was decided against the head and never checked against the second request's cap.
func TestABurnDecidedForAHeadThatLeavesIsNotChargedToARequestAtItsCap(t *testing.T) {
	var abandonOnce sync.Once
	var head *waiter
	test := newHarness(t, harnessConfig{
		holdStart: true, maxBurnsPerRequest: 2, ejected: func(participant string) bool { return participant == hostB },
		afterDecide: func(HostBinding) { abandonOnce.Do(func() { head.abandoned.Store(true) }) },
	})
	head = test.submitCosting(t, RequestProfile{RequestID: "request-head", Model: modelA, Params: "payload", Burns: &BurnTally{}}, test.clock.Now())
	atCap := chargedTally(2)
	next := test.submitCosting(t, RequestProfile{RequestID: "request-next", Model: modelA, Params: "payload", Burns: atCap}, test.clock.Now())

	test.dispatcher.start()

	wantAssignment(t, awaitReply(t, next), hostA, 2)
	test.dispatcher.stop()
	require.EqualValues(t, 2, atCap.burned.Load())
}
