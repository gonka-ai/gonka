package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/scheduler"
)

func cappedPolicy(maxStartedAttempts int) EscalationPolicy {
	policy := refusalPolicy()
	policy.MaxStartedAttempts = maxStartedAttempts
	return policy
}

func (p *stubPicker) pickCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.profiles)
}

// Test flow:
//  1. Build a policy for a four-host group with a cap of three started attempts, then the same with the cap off, one host, and nonces scarce.
//  2. Ask each for its `AttemptLimit`.
//  3. Assert the cap lowers the limit below the group, never raises it past the group, is ignored when zero, and loses to the scarce-nonce limit of one.
func TestTheStartedAttemptsCapLowersTheAttemptLimitBelowTheHostGroup(t *testing.T) {
	testCases := []struct {
		name        string
		cap         int
		hostCount   int
		nonceScarce bool
		want        int
	}{
		{name: "a cap below the group", cap: 3, hostCount: 4, want: 3},
		{name: "a cap above the group", cap: 3, hostCount: 2, want: 2},
		{name: "a cap of zero", cap: 0, hostCount: 4, want: 4},
		{name: "scarce nonces", cap: 3, hostCount: 4, nonceScarce: true, want: 1},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			policy := EscalationPolicy{MaxStartedAttempts: testCase.cap}
			require.Equal(t, testCase.want, policy.AttemptLimit(testCase.hostCount, testCase.nonceScarce))
		})
	}
}

// Test flow:
//  1. Start a race under a cap of two started attempts over four hosts whose first three would each fail with a connection reset.
//  2. Run the race to completion.
//  3. Assert exactly two attempts started and two picks were made, and the race returned the cap error since no attempt was alive when the third was refused.
func TestARetryPastTheAttemptCapAnswersTheRequestCappedWhenNoAttemptIsAlive(t *testing.T) {
	fixture := newRaceFixture(cappedPolicy(2), 4)
	for index, nonce := range []uint64{200, 201, 202} {
		fixture.host(nonce, index, "host-"+string(rune('a'+index)), &hostScript{receipt: true, err: errors.New("connection reset")})
	}

	outcome, err := fixture.run(context.Background())
	<-fixture.reported

	require.ErrorIs(t, err, scheduler.ErrRequestCapped)
	require.Len(t, outcome.Attempts, 2)
	require.Equal(t, 2, fixture.picker.pickCount())
}

// Test flow:
//  1. Start the same race with the cap off.
//  2. Run it to completion.
//  3. Assert all three attempts started and the race ended unsuccessful without the cap error.
func TestWithoutTheAttemptCapTheRetriesRunPastTwoAttempts(t *testing.T) {
	fixture := newRaceFixture(cappedPolicy(0), 4)
	for index, nonce := range []uint64{210, 211, 212} {
		fixture.host(nonce, index, "host-"+string(rune('a'+index)), &hostScript{receipt: true, err: errors.New("connection reset")})
	}

	outcome, err := fixture.run(context.Background())
	<-fixture.reported

	require.NoError(t, err)
	require.Len(t, outcome.Attempts, 3)
}

// Test flow:
//  1. Start a race under a cap of one started attempt over four hosts whose one queued host is held before its receipt, so a hedge is due at once.
//  2. Let the attempt arrive, then release it to answer with content.
//  3. Assert the hedge was never launched and no pick was made for it, and the live attempt won, since a cap never fails a request that has an attempt alive.
func TestAHedgePastTheAttemptCapIsNotLaunchedWhileTheFirstAttemptRuns(t *testing.T) {
	fixture := newRaceFixture(cappedPolicy(1), 4)
	arrive := make(chan uint64, 1)
	release := make(chan struct{})
	fixture.host(220, 0, "host-a", &hostScript{
		arrive: arrive, release: release, receipt: true,
		chunks: []string{contentChunk(220)}, confirmed: true, finished: true,
	})

	outcomes := make(chan RaceOutcome, 1)
	go func() {
		outcome, err := fixture.run(context.Background())
		if err != nil {
			t.Error(err)
		}
		outcomes <- outcome
	}()
	waitForValue(t, arrive, "the first attempt reaching its host")
	close(release)

	outcome := waitForValue(t, outcomes, "the race ending")
	waitForValue(t, fixture.reported, "the race's report")
	require.True(t, outcome.Succeeded)
	require.Equal(t, uint64(220), outcome.WinnerNonce)
	require.Len(t, outcome.Attempts, 1)
	require.Equal(t, 1, fixture.picker.pickCount())
}

// Test flow:
//  1. Start a race whose scheduler answers the burn-capped error to every pick after the first, with a first attempt held before its receipt so a hedge is due at once.
//  2. Let the first attempt arrive, wait for the hedge pick to be refused, then release the first attempt to answer with content.
//  3. Assert the hedge pick was made and refused, no second attempt started, and the first attempt won with no error, since a capped hedge leaves the live attempt running.
func TestAHedgeRefusedByTheBurnCapLeavesTheFirstAttemptToWin(t *testing.T) {
	fixture := newRaceFixture(racePolicy(2), 4)
	fixture.picker.refusal = scheduler.ErrRequestCapped
	arrive := make(chan uint64, 1)
	release := make(chan struct{})
	fixture.host(230, 0, "host-a", &hostScript{
		arrive: arrive, release: release, receipt: true,
		chunks: []string{contentChunk(230)}, confirmed: true, finished: true,
	})

	outcomes := make(chan RaceOutcome, 1)
	go func() {
		outcome, err := fixture.run(context.Background())
		if err != nil {
			t.Error(err)
		}
		outcomes <- outcome
	}()
	waitForValue(t, arrive, "the first attempt reaching its host")
	waitForPicks(t, fixture.picker, 2, "the hedge pick")
	close(release)

	outcome := waitForValue(t, outcomes, "the race ending")
	waitForValue(t, fixture.reported, "the race's report")
	require.True(t, outcome.Succeeded)
	require.Equal(t, uint64(230), outcome.WinnerNonce)
	require.Len(t, outcome.Attempts, 1)
}

// Test flow:
//  1. Start a race whose first attempt fails with a connection reset and whose scheduler answers the burn-capped error to the retry.
//  2. Run the race to completion.
//  3. Assert the race returned the cap error with the failed attempt in its outcome, since no attempt was alive when the retry was refused.
func TestARetryRefusedByTheBurnCapAnswersTheRequestCappedWhenNoAttemptIsAlive(t *testing.T) {
	fixture := newRaceFixture(refusalPolicy(), 4)
	fixture.picker.refusal = scheduler.ErrRequestCapped
	fixture.host(240, 0, "host-a", &hostScript{receipt: true, err: errors.New("connection reset")})

	outcome, err := fixture.run(context.Background())
	<-fixture.reported

	require.ErrorIs(t, err, scheduler.ErrRequestCapped)
	require.Len(t, outcome.Attempts, 1)
	require.False(t, outcome.Succeeded)
}

func waitForPicks(t *testing.T, picker *stubPicker, count int, what string) {
	t.Helper()
	require.Eventually(t, func() bool { return picker.pickCount() >= count }, 5*time.Second, time.Millisecond, "timed out waiting for %s", what)
}

// Test flow:
//  1. Build a paused coordinator whose burn cap refused a hedge while its attempt was alive.
//  2. Mark the retry ruled out, as a trusted host's rejection does, and ask whether the cap refused the request.
//  3. Assert it did not, so the host's own answer reaches the client rather than a capped refusal that invites a retry.
func TestARejectionThatRulesOutARetryIsNotHiddenBehindTheBurnCap(t *testing.T) {
	coordinator := pausedCoordinator(newRaceFixture(settledPolicy(), 2), 2)
	coordinator.burnCapReached = true

	require.True(t, coordinator.refusedByCap())

	coordinator.retryRuledOut = true

	require.False(t, coordinator.refusedByCap())
}

// Test flow:
//  1. Start a race whose scheduler answers the burn-capped error to every pick after the first, with a first attempt held before its receipt so a hedge is due at once.
//  2. Let the first attempt arrive, wait for the hedge pick to be refused, then release the first attempt to answer with a 422 error event that streamed no content.
//  3. Assert the race ends without the cap error, since the host answered the request and no retry was owed.
func TestAHedgeRefusedByTheBurnCapDoesNotHideTheFirstAttemptsAnswer(t *testing.T) {
	fixture := newRaceFixture(racePolicy(2), 4)
	fixture.picker.refusal = scheduler.ErrRequestCapped
	arrive := make(chan uint64, 1)
	release := make(chan struct{})
	fixture.host(250, 0, "host-a", &hostScript{
		arrive: arrive, release: release, receipt: true,
		chunks:    []string{"data: unprocessable-request\n\n"},
		confirmed: true, finished: true,
	})

	outcomes := make(chan error, 1)
	go func() {
		_, err := fixture.run(context.Background())
		outcomes <- err
	}()
	waitForValue(t, arrive, "the first attempt reaching its host")
	waitForPicks(t, fixture.picker, 2, "the hedge pick")
	close(release)

	err := waitForValue(t, outcomes, "the race ending")
	waitForValue(t, fixture.reported, "the race's report")
	require.NotErrorIs(t, err, scheduler.ErrRequestCapped)
}
