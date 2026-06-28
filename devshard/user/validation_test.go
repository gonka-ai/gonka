package user

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

// rejectingValidator returns Valid=false. delay simulates ML re-execution
// time so the first finisher's MsgValidation propagates before slower
// validators emit (matches production timing).
type rejectingValidator struct {
	inflight atomic.Int64
	calls    atomic.Uint64
	delay    time.Duration
}

func (v *rejectingValidator) Validate(_ context.Context, _ devshard.ValidateRequest) (*devshard.ValidateResult, error) {
	v.inflight.Add(1)
	defer v.inflight.Add(-1)
	v.calls.Add(1)
	if v.delay > 0 {
		time.Sleep(v.delay)
	}
	return &devshard.ValidateResult{Valid: false}, nil
}

// Fixed keys keep ownSeed deterministic so ShouldValidate is reproducible.
var validationTestHostKeys = []string{
	"1111111111111111111111111111111111111111111111111111111111111111",
	"2222222222222222222222222222222222222222222222222222222222222222",
	"3333333333333333333333333333333333333333333333333333333333333333",
	"4444444444444444444444444444444444444444444444444444444444444444",
	"5555555555555555555555555555555555555555555555555555555555555555",
}

const validationTestUserKey = "9999999999999999999999999999999999999999999999999999999999999999"

func sumHostStatsInvalid(st types.EscrowState) uint32 {
	var n uint32
	for _, hs := range st.HostStats {
		n += hs.Invalid
	}
	return n
}

func TestSession_Validation_InvalidationConverges(t *testing.T) {
	const numHosts = 5
	const numInferences = 100
	const balance = 10_000_000
	grace := uint64(numInferences + 100)

	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i, hex := range validationTestHostKeys {
		hosts[i] = testutil.MustSignerFromHex(t, hex)
	}
	user := testutil.MustSignerFromHex(t, validationTestUserKey)

	group := testutil.MakeGroup(hosts)
	config := types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		VoteThreshold:    1,
		ValidationRate:   10000,
	}
	verifier := signing.NewSecp256k1Verifier()

	// Staggered delays: slot 0 finishes first and challenges; others
	// emit MsgValidationVote when their goroutine wakes after the
	// challenge has propagated.
	validators := make([]*rejectingValidator, numHosts)
	for i := range validators {
		validators[i] = &rejectingValidator{delay: time.Duration(50+i*100) * time.Millisecond}
	}

	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm, err := state.NewStateMachine("escrow-validation", config, group, balance, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-validation", user.Address(), config, group, balance))
		require.NoError(t, err)
		h, err := host.NewHost(
			sm, hosts[i], stub.NewInferenceEngine(),
			"escrow-validation", group, nil,
			host.WithGrace(grace), host.WithValidator(validators[i]),
		)
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	userSM, err := state.NewStateMachine("escrow-validation", config, group, balance, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-validation", user.Address(), config, group, balance))
	require.NoError(t, err)
	session, err := NewSession(userSM, user, "escrow-validation", group, clients, verifier)
	require.NoError(t, err)

	ctx := context.Background()
	params := InferenceParams{
		Model:       "llama",
		Prompt:      testutil.TestPrompt,
		InputLength: 100,
		MaxTokens:   50,
	}
	for i := 1; i <= numInferences; i++ {
		params.StartedAt = int64(i) * 1000
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err, "inference %d", i)
	}

	// Keep the round-robin going so challenges reach slower validators
	// before they wake from Validate.
	allDrained := func() bool {
		for _, v := range validators {
			if v.inflight.Load() != 0 {
				return false
			}
		}
		return true
	}
	deadline := time.Now().Add(30 * time.Second)
	for !allDrained() {
		require.NoError(t, session.SendPendingDiff(ctx))
		require.False(t, time.Now().After(deadline), "validate goroutines did not drain")
	}
	time.Sleep(100 * time.Millisecond)
	for i := 0; i < 2*numHosts; i++ {
		require.NoError(t, session.SendPendingDiff(ctx))
	}

	// Snapshot before finalize. Terminal outcomes may already be auto-sealed
	// out of the live map; ExportAllInferenceRecords includes those snapshots.
	preFinalize := session.StateMachine().SnapshotState()
	allRecords := session.StateMachine().ExportAllInferenceRecords()
	var finished, challenged, invalidated, validated, other int
	for _, rec := range allRecords {
		switch rec.Status {
		case types.StatusFinished:
			finished++
		case types.StatusChallenged:
			challenged++
		case types.StatusInvalidated:
			invalidated++
		case types.StatusValidated:
			validated++
		default:
			other++
		}
	}
	totalHostStatsInvalid := sumHostStatsInvalid(preFinalize)
	var totalCalls uint64
	for _, v := range validators {
		totalCalls += v.calls.Load()
	}
	hist := fmt.Sprintf(
		"histogram: live=%d records=%d finished=%d challenged=%d invalidated=%d validated=%d other=%d host_stats_invalid=%d validate_calls=%d",
		len(preFinalize.Inferences), len(allRecords), finished, challenged, invalidated, validated, other,
		totalHostStatsInvalid, totalCalls,
	)
	t.Log(hist)

	require.Greater(t, challenged+invalidated, 0, "validators never produced any MsgValidation; %s", hist)
	require.GreaterOrEqual(t, invalidated, 10, hist)
	require.Greater(t, totalHostStatsInvalid, uint32(0), hist)

	require.NoError(t, session.Finalize(ctx))
	st := session.StateMachine().SnapshotState()
	require.Equal(t, types.PhaseSettlement, st.Phase)
	require.Empty(t, st.Inferences, "live map must be empty after settlement drain")
	require.Equal(t, numInferences, len(session.StateMachine().ExportSealedNonces()))
}

// TestSession_Validation_MultiSlotValidatorCountedOnce verifies that a host
// owning N slots calls Validate once per inference and is credited with
// weight N in the state machine.
func TestSession_Validation_MultiSlotValidatorCountedOnce(t *testing.T) {
	const balance = 10_000_000
	const numInferences = 30
	grace := uint64(numInferences + 100)

	hosts := []*signing.Secp256k1Signer{
		testutil.MustSignerFromHex(t, validationTestHostKeys[0]),
		testutil.MustSignerFromHex(t, validationTestHostKeys[1]),
		testutil.MustSignerFromHex(t, validationTestHostKeys[2]),
	}
	user := testutil.MustSignerFromHex(t, validationTestUserKey)

	// host0=slot0 (weight 1), host1=slots 1..3 (mega, weight 3), host2=slot4 (weight 1).
	group := testutil.MakeMultiSlotGroup(hosts, []int{1, 3, 1})
	require.Len(t, group, 5)

	config := types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		VoteThreshold:    2,
		ValidationRate:   10000,
	}
	verifier := signing.NewSecp256k1Verifier()

	validators := make([]*rejectingValidator, len(hosts))
	for i := range validators {
		validators[i] = &rejectingValidator{delay: time.Duration(50+i*100) * time.Millisecond}
	}

	// One Host per signer (mega owns 3 slots in a single host instance).
	// All clients for mega's slots dispatch to the same Host so Validate
	// runs once per inference, not three times.
	hostBySigner := make([]*host.Host, len(hosts))
	for i := range hosts {
		sm, err := state.NewStateMachine("escrow-multi", config, group, balance, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-multi", user.Address(), config, group, balance))
		require.NoError(t, err)
		h, err := host.NewHost(
			sm, hosts[i], stub.NewInferenceEngine(),
			"escrow-multi", group, nil,
			host.WithGrace(grace), host.WithValidator(validators[i]),
		)
		require.NoError(t, err)
		hostBySigner[i] = h
	}
	clients := make([]HostClient, len(group))
	for i, slot := range group {
		for j, signer := range hosts {
			if signer.Address() == slot.ValidatorAddress {
				clients[i] = &InProcessClient{Host: hostBySigner[j]}
				break
			}
		}
		require.NotNil(t, clients[i], "no client for slot %d", i)
	}

	userSM, err := state.NewStateMachine("escrow-multi", config, group, balance, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-multi", user.Address(), config, group, balance))
	require.NoError(t, err)
	session, err := NewSession(userSM, user, "escrow-multi", group, clients, verifier)
	require.NoError(t, err)

	ctx := context.Background()
	params := InferenceParams{
		Model:       "llama",
		Prompt:      testutil.TestPrompt,
		InputLength: 100,
		MaxTokens:   50,
	}
	for i := 1; i <= numInferences; i++ {
		params.StartedAt = int64(i) * 1000
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err, "inference %d", i)
	}

	allDrained := func() bool {
		for _, v := range validators {
			if v.inflight.Load() != 0 {
				return false
			}
		}
		return true
	}
	deadline := time.Now().Add(30 * time.Second)
	for !allDrained() {
		require.NoError(t, session.SendPendingDiff(ctx))
		require.False(t, time.Now().After(deadline), "validate goroutines did not drain")
	}
	time.Sleep(100 * time.Millisecond)
	for i := 0; i < 2*len(hosts); i++ {
		require.NoError(t, session.SendPendingDiff(ctx))
	}

	// Terminal invalidations are auto-sealed out of the live map before
	// finalize; ExportAllInferenceRecords includes sealed snapshots.
	allRecords := session.StateMachine().ExportAllInferenceRecords()
	megaSlots := []uint32{1, 2, 3}

	megaParticipations := 0
	invalidated := 0
	for _, rec := range allRecords {
		participated := false
		for _, slot := range megaSlots {
			if rec.ValidatedBy.IsSet(slot) {
				participated = true
				break
			}
		}
		if participated {
			megaParticipations++
		}

		// Invalidation requires VotesInvalid > VoteThreshold(2), i.e. >= 3.
		// host0 + host2 together have weight 1+1 = 2 (mega excluded as
		// executor). So every Invalidated inference must include mega's
		// weight 3 in VotesInvalid.
		if rec.Status == types.StatusInvalidated {
			invalidated++
			require.GreaterOrEqual(t, rec.VotesInvalid, uint32(3),
				"invalidated inference exec=%d has VotesInvalid=%d < mega weight 3",
				rec.ExecutorSlot, rec.VotesInvalid)
		}
	}

	require.Greater(t, invalidated, 0, "expected at least one invalidated inference")

	// One physical Validate per inference mega participated in, regardless
	// of slot count.
	require.Equal(t, uint64(megaParticipations), validators[1].calls.Load(),
		"mega should run Validate once per participation, got %d calls for %d participations",
		validators[1].calls.Load(), megaParticipations)

	require.NoError(t, session.Finalize(ctx))
	st := session.StateMachine().SnapshotState()
	require.Empty(t, st.Inferences)
	require.Greater(t, sumHostStatsInvalid(st), uint32(0))
	require.Equal(t, numInferences, len(session.StateMachine().ExportSealedNonces()))
}
