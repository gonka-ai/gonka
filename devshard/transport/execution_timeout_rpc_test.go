package transport_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/transport"
	"devshard/types"
)

// newExecutionTimeoutServer is a verifier whose one inference was started by
// slot 1 long ago and has no finish.
func newExecutionTimeoutServer(t *testing.T) *transport.Server {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(group))
	verifier := signing.NewSecp256k1Verifier()
	const escrow = "escrow-execution-timeout"

	store, err := storage.NewSQLite(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       escrow,
		EpochID:        1,
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	sm, err := state.NewStateMachine(escrow, config, group, 100000, user.Address(), verifier,
		testutil.MustMemoryStore(t, escrow, user.Address(), config, group, 100000))
	require.NoError(t, err)
	st := sm.ExportState()
	hash := testutil.TestPromptHash
	st.Inferences = map[uint64]*types.InferenceRecord{
		1: {
			Status:             types.StatusStarted,
			ExecutorSlot:       1,
			ReservedCost:       150,
			StartedAtHeight:    1,
			StartedAtBlockHash: []byte{0x01},
			StartedAtTimeMs:    testutil.HeightClockBlockMs,
			ConfirmedAtHeight:  1,
			ConfirmedAtTimeMs:  testutil.HeightClockBlockMs,
			PromptHash:         hash[:],
			Model:              "llama",
			InputLength:        100,
			MaxTokens:          testutil.TestMaxTokens,
		},
	}
	// A restore at nonce 0 drops the floor; observe one block past
	// ConfirmedAtHeight + ExecutionTimeout (1 s per block) at nonce 1.
	st.LatestNonce = 1
	floor := heightsync.NewFloorIndex()
	floor.Observe(st.LatestNonce, []heightsync.FloorClaim{{Signer: 0, Height: 1 + uint64(config.ExecutionTimeout), Hash: []byte{0x02}}})
	require.NoError(t, sm.RestoreStateWithFloor(st, floor))
	require.NoError(t, sm.TimeoutDue(1, types.TimeoutReason_TIMEOUT_REASON_EXECUTION))

	h, err := host.NewHost(sm, hosts[0], stub.NewInferenceEngine(), escrow, group, nil, host.WithStorage(store))
	require.NoError(t, err)
	t.Cleanup(h.Close)

	srv, err := transport.NewServer(h, store, verifier, user.Address())
	require.NoError(t, err)
	return srv
}

// TestExecutionTimeoutHeldChallengeVotesInBudget: an executor that holds the
// execution-timeout ChallengeReceipt open is unreachable after the verify
// budget, and the vote returns before the caller's own deadline.
func TestExecutionTimeoutHeldChallengeVotesInBudget(t *testing.T) {
	srv := newExecutionTimeoutServer(t)
	exec := &recordingExecutor{t: t, block: true, verifyBudget: 200 * time.Millisecond}
	srv.SetPeerClients(map[int]transport.HostPeerClient{1: exec})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	resp, err := srv.ServeVerifyTimeout(ctx, transport.VerifyTimeoutRequest{
		InferenceID: 1,
		Reason:      transport.TimeoutReasonToString(types.TimeoutReason_TIMEOUT_REASON_EXECUTION),
	})
	require.NoError(t, err)
	require.True(t, resp.Accept)
	require.Less(t, time.Since(start), 2*time.Second)
}
