package user

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

func setupSession(t *testing.T, numHosts int, balance uint64, grace uint64) (*Session, []*signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	return setupSessionWithEngine(t, numHosts, balance, grace, nil)
}

// setupSessionWithOptions is like setupSession but forwards extra options to
// NewSession. Useful for tests that want to inject a private verifier queue
// so they don't share process-wide state with other parallel tests.
func setupSessionWithOptions(t *testing.T, numHosts int, balance uint64, grace uint64, opts ...SessionOption) (*Session, []*signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm := statetest.MustStateMachine(t, "escrow-1", config, group, balance, user.Address(), verifier)
		engine := stub.NewInferenceEngine()
		h, err := host.NewHost(sm, hosts[i], engine, "escrow-1", group, nil, host.WithGrace(grace))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, balance, user.Address(), verifier)
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier, opts...)
	require.NoError(t, err)

	return session, hosts, user
}

func setupSessionWithEngine(t *testing.T, numHosts int, balance uint64, grace uint64, engines []devshard.InferenceEngine) (*Session, []*signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	// Create hosts.
	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm := statetest.MustStateMachine(t, "escrow-1", config, group, balance, user.Address(), verifier)
		var engine devshard.InferenceEngine
		if engines != nil {
			engine = engines[i]
		} else {
			engine = stub.NewInferenceEngine()
		}
		h, err := host.NewHost(sm, hosts[i], engine, "escrow-1", group, nil, host.WithGrace(grace))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	// Create user session.
	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, balance, user.Address(), verifier)
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier)
	require.NoError(t, err)

	return session, hosts, user
}

func TestUser_RoundRobinSelection(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()

	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	// Nonce 1 -> host 1%3=1, nonce 2 -> host 2%3=2, nonce 3 -> host 3%3=0.
	for i := 0; i < 6; i++ {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	// Verify round-robin pattern over 6 inferences.
	require.Equal(t, uint64(6), session.Nonce())
}

func TestPrepareInference_StartInferenceIsMandatory(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100, 10)

	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	prepared, err := session.PrepareInference(params)
	require.Error(t, err)
	require.Nil(t, prepared)
	require.ErrorContains(t, err, "mandatory start inference")
	require.ErrorIs(t, err, types.ErrInsufficientBalance)
	require.Equal(t, uint64(0), session.Nonce(), "failed start must not advance nonce")
	require.Empty(t, session.Diffs(), "failed start must not record a no-start diff")
	_, ok := session.sm.GetInference(1)
	require.False(t, ok, "failed start must not create an inference record")
}

func TestUser_PipelinesReceipt(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()

	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	// First inference.
	result1, err := session.SendInference(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result1.Receipt, "executor should return receipt")

	// After processing response, pendingTxs should have MsgConfirmStart + MsgFinishInference.
	// Second inference should pipeline these.
	_, err = session.SendInference(ctx, params)
	require.NoError(t, err)

	// Find MsgConfirmStart in diff at nonce 2.
	diff2 := session.Diffs()[1]
	var hasConfirm bool
	for _, tx := range diff2.Txs {
		if confirm := tx.GetConfirmStart(); confirm != nil {
			require.Equal(t, uint64(1), confirm.InferenceId)
			hasConfirm = true
		}
	}
	require.True(t, hasConfirm, "diff 2 should pipeline MsgConfirmStart for inference 1")
}

func TestUser_CollectsSignatures(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()

	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	_, err := session.SendInference(ctx, params)
	require.NoError(t, err)

	sigs := session.Signatures()
	require.NotEmpty(t, sigs, "should have signatures")

	// The contacted host (slot 1 for nonce 1) should have signed.
	nonce1Sigs, ok := sigs[1]
	require.True(t, ok, "should have sigs for nonce 1")
	require.NotNil(t, nonce1Sigs[1], "slot 1 should have signed")
}

// ErrorClient always returns an error.
type ErrorClient struct {
	Err error
}

func (c *ErrorClient) Send(_ context.Context, _ host.HostRequest, _ io.Writer, _ func(*host.HostResponse)) (*host.HostResponse, error) {
	return nil, c.Err
}

func TestUser_HostError_StateConsistency(t *testing.T) {
	numHosts := 3
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	// Create real hosts for slots 0 and 2, error client for slot 1.
	clients := make([]HostClient, numHosts)
	for i := range hosts {
		if i == 1 {
			clients[i] = &ErrorClient{Err: fmt.Errorf("host unavailable")}
			continue
		}
		sm := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, userKey.Address(), verifier)
		engine := stub.NewInferenceEngine()
		h, err := host.NewHost(sm, hosts[i], engine, "escrow-1", group, nil, host.WithGrace(100))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, userKey.Address(), verifier)
	session, err := NewSession(userSM, userKey, "escrow-1", group, clients, verifier)
	require.NoError(t, err)

	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	// Nonce 1 -> host 1 (error client). Should fail.
	_, err = session.SendInference(ctx, params)
	require.Error(t, err, "send to error host should fail")

	// User's local state should have advanced (diff was applied locally before send).
	require.Equal(t, uint64(1), session.Nonce(), "nonce should have advanced")
	require.Len(t, session.Diffs(), 1, "diff should be recorded")

	// Next inference (nonce 2) -> host 2 (working). Should succeed with catch-up.
	result, err := session.SendInference(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, uint64(2), session.Nonce())
}

func TestSession_SendPendingDiffRetriesReachableHost(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	// Advance to nonce 3 so the next pending diff targets slot 1.
	for range 3 {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}
	session.clients[1] = &ErrorClient{Err: fmt.Errorf("host unavailable")}

	require.NoError(t, session.SendPendingDiff(ctx))
	require.Equal(t, uint64(4), session.Nonce())

	// Slot 1 is the deterministic target for nonce 4. The retry must deliver
	// the diff, including its catch-up history, to the next reachable slot.
	fallback := session.clients[2].(*InProcessClient)
	require.Equal(t, uint64(4), fallback.Host.SnapshotState().LatestNonce)
}

func TestUser_Finalize(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 100)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	for i := 0; i < 3; i++ {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	err := session.Finalize(ctx)
	require.NoError(t, err)

	st := session.StateMachine().SnapshotState()
	require.True(t, st.Phase >= types.PhaseFinalizing)
	for id, rec := range st.Inferences {
		require.Equal(t, types.StatusFinished, rec.Status, "inference %d should be finished", id)
	}
}

func TestUser_Finalize_CollectsSignatures(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 100)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	for i := 0; i < 3; i++ {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	err := session.Finalize(ctx)
	require.NoError(t, err)

	// Phase B visits all 3 hosts. Each should have signed at some nonce.
	sigs := session.Signatures()
	signedSlots := make(map[uint32]bool)
	for _, slotSigs := range sigs {
		for slotID := range slotSigs {
			signedSlots[slotID] = true
		}
	}
	for i := uint32(0); i < 3; i++ {
		require.True(t, signedSlots[i], "slot %d should have signed at least once", i)
	}
}

func TestUser_Finalize_DiffCount(t *testing.T) {
	numHosts := 3
	session, _, _ := setupSession(t, numHosts, 100000, 100)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	for i := 0; i < 3; i++ {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}
	preFinalize := len(session.Diffs())

	err := session.Finalize(ctx)
	require.NoError(t, err)

	// Finalize adds N (Phase A) + 1 (drain) = N + 1. Phase B sends catch-up only.
	expected := preFinalize + numHosts + 1
	require.Equal(t, expected, len(session.Diffs()),
		"total diffs = pre-finalize(%d) + N+1(%d)", preFinalize, numHosts+1)
}

func TestUser_PendingTxDedup(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 100)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	// Send one inference to populate host mempool.
	resp, err := session.SendInference(ctx, params)
	require.NoError(t, err)

	// ProcessResponse already queued mempool txs. Record count.
	countBefore := len(session.PendingTxs())

	// Simulate receiving the same mempool again (as if from another host).
	err = session.ProcessResponse(0, &host.HostResponse{
		Nonce:   resp.Nonce,
		Mempool: resp.Mempool,
	}, resp.Nonce)
	require.NoError(t, err)

	// Dedup should prevent growth.
	require.Equal(t, countBefore, len(session.PendingTxs()),
		"duplicate mempool txs should be deduplicated")
}

func TestPendingTxDedupKeys_HostProposedIdentity(t *testing.T) {
	session := &Session{
		pendingTxKeys: make(map[string]struct{}),
		appliedTxKeys: make(map[string]struct{}),
	}

	keyed := []struct {
		name string
		tx   *types.DevshardTx
		key  string
	}{
		{
			name: "finish",
			tx: &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{
				FinishInference: &types.MsgFinishInference{InferenceId: 7},
			}},
			key: "finish:7",
		},
		{
			name: "confirm",
			tx: &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{
				ConfirmStart: &types.MsgConfirmStart{InferenceId: 7},
			}},
			key: "confirm:7",
		},
		{
			name: "validation",
			tx: &types.DevshardTx{Tx: &types.DevshardTx_Validation{
				Validation: &types.MsgValidation{InferenceId: 7, ValidatorSlot: 2},
			}},
			key: "validation:7:2",
		},
		{
			name: "validation_vote",
			tx: &types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{
				ValidationVote: &types.MsgValidationVote{InferenceId: 7, VoterSlot: 2},
			}},
			key: "vote:7:2",
		},
		{
			name: "reveal_seed",
			tx: &types.DevshardTx{Tx: &types.DevshardTx_RevealSeed{
				RevealSeed: &types.MsgRevealSeed{SlotId: 2},
			}},
			key: "reveal_seed:2",
		},
		{
			name: "height_ack",
			tx: &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{
				HeightAck: &types.MsgHeightAck{RefNonce: 5, SlotId: 2},
			}},
			key: "height_ack:5:2",
		},
	}

	for _, tc := range keyed {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.key, devshardTxKey(tc.tx))
			before := len(session.PendingTxs())
			session.addPendingTx(tc.tx)
			require.Len(t, session.PendingTxs(), before+1)
			session.addPendingTx(tc.tx)
			require.Len(t, session.PendingTxs(), before+1,
				"same host-proposed tx key must not be queued twice")

			// Draining the queue without applying anything must release the
			// key, so an honest tx is not shadowed by one that never landed.
			session.clearPendingTxs()
			session.addPendingTx(tc.tx)
			require.Len(t, session.PendingTxs(), 1,
				"a key that never applied must be reusable")

			// Once it applies, the key is retained and later mempool copies
			// from other hosts are ignored.
			session.retainPendingLocked(nil, []*types.DevshardTx{tc.tx})
			session.addPendingTx(tc.tx)
			require.Empty(t, session.PendingTxs(),
				"an applied tx must not be re-queued from another host mempool")
			session.clearPendingTxs()
		})
	}

	session.addPendingTx(&types.DevshardTx{Tx: &types.DevshardTx_HeightAck{
		HeightAck: &types.MsgHeightAck{RefNonce: 5, SlotId: 3},
	}})
	session.addPendingTx(&types.DevshardTx{Tx: &types.DevshardTx_HeightAck{
		HeightAck: &types.MsgHeightAck{RefNonce: 6, SlotId: 2},
	}})
	require.Len(t, session.PendingTxs(), 2,
		"height ack dedup identity is ref_nonce plus slot")

	unkeyedStart := &types.DevshardTx{Tx: &types.DevshardTx_StartInference{
		StartInference: &types.MsgStartInference{InferenceId: 7},
	}}
	unkeyedHeartbeat := &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{
		Heartbeat: &types.MsgHeartbeat{SlotsNum: 3},
	}}
	require.Empty(t, devshardTxKey(unkeyedStart))
	require.Empty(t, devshardTxKey(unkeyedHeartbeat))
	before := len(session.PendingTxs())
	session.addPendingTx(unkeyedStart)
	session.addPendingTx(unkeyedStart)
	session.addPendingTx(unkeyedHeartbeat)
	session.addPendingTx(unkeyedHeartbeat)
	require.Len(t, session.PendingTxs(), before+4,
		"user-authored/unkeyed txs are outside host-proposed pending dedup")
}

func TestHostMayProposeTx(t *testing.T) {
	require.False(t, hostMayProposeTx(nil))
	require.False(t, hostMayProposeTx(&types.DevshardTx{}))
	require.False(t, hostMayProposeTx(&types.DevshardTx{Tx: &types.DevshardTx_StartInference{
		StartInference: &types.MsgStartInference{InferenceId: 2},
	}}))
	require.False(t, hostMayProposeTx(&types.DevshardTx{Tx: &types.DevshardTx_TimeoutInference{
		TimeoutInference: &types.MsgTimeoutInference{InferenceId: 1},
	}}))
	require.False(t, hostMayProposeTx(&types.DevshardTx{Tx: &types.DevshardTx_FinalizeRound{
		FinalizeRound: &types.MsgFinalizeRound{},
	}}))
	require.False(t, hostMayProposeTx(&types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{
		Heartbeat: &types.MsgHeartbeat{},
	}}))
	require.False(t, hostMayProposeTx(&types.DevshardTx{Tx: &types.DevshardTx_ForceHeightSyncTurn{
		ForceHeightSyncTurn: &types.MsgForceHeightSyncTurn{TriggerNonce: 1},
	}}))
	require.False(t, hostMayProposeTx(&types.DevshardTx{Tx: &types.DevshardTx_ErrorMiss{
		ErrorMiss: &types.MsgErrorMiss{InferenceId: 1},
	}}))
	require.False(t, hostMayProposeTx(&types.DevshardTx{Tx: &types.DevshardTx_FinishInference{}}),
		"nil inner Finish must not pass the allowlist")
	require.True(t, hostMayProposeTx(&types.DevshardTx{Tx: &types.DevshardTx_FinishInference{
		FinishInference: &types.MsgFinishInference{InferenceId: 1},
	}}))
	require.True(t, hostMayProposeTx(&types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{
		ConfirmStart: &types.MsgConfirmStart{InferenceId: 1},
	}}))
	require.True(t, hostMayProposeTx(&types.DevshardTx{Tx: &types.DevshardTx_Validation{
		Validation: &types.MsgValidation{InferenceId: 1, ValidatorSlot: 2},
	}}))
	require.True(t, hostMayProposeTx(&types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{
		ValidationVote: &types.MsgValidationVote{InferenceId: 1, VoterSlot: 2},
	}}))
	require.True(t, hostMayProposeTx(&types.DevshardTx{Tx: &types.DevshardTx_HeightAck{
		HeightAck: &types.MsgHeightAck{RefNonce: 1, SlotId: 0},
	}}))
}

func TestDevshardTxKey_NilInnerDoesNotPanic(t *testing.T) {
	require.Empty(t, devshardTxKey(nil))
	require.Empty(t, devshardTxKey(&types.DevshardTx{}))
	require.Empty(t, devshardTxKey(&types.DevshardTx{Tx: &types.DevshardTx_FinishInference{}}))
	require.Empty(t, devshardTxKey(&types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{}}))
	require.Empty(t, devshardTxKey(&types.DevshardTx{Tx: &types.DevshardTx_Validation{}}))
	require.Empty(t, devshardTxKey(&types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{}}))
	require.Empty(t, devshardTxKey(&types.DevshardTx{Tx: &types.DevshardTx_RevealSeed{}}))
	require.Empty(t, devshardTxKey(&types.DevshardTx{Tx: &types.DevshardTx_HeightAck{}}))
	require.Equal(t, "finish:7", devshardTxKey(&types.DevshardTx{Tx: &types.DevshardTx_FinishInference{
		FinishInference: &types.MsgFinishInference{InferenceId: 7},
	}}))
}

func TestProcessResponse_DropsUserProposedMempoolTxs(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 100)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	_, err := session.SendInference(ctx, params)
	require.NoError(t, err)

	injectedStart := injectedStartTx(2)
	injectedTimeout := &types.DevshardTx{Tx: &types.DevshardTx_TimeoutInference{
		TimeoutInference: &types.MsgTimeoutInference{InferenceId: 1},
	}}
	injectedFinalize := &types.DevshardTx{Tx: &types.DevshardTx_FinalizeRound{
		FinalizeRound: &types.MsgFinalizeRound{},
	}}
	honestAck := &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{
		HeightAck: &types.MsgHeightAck{RefNonce: 1, SlotId: 0},
	}}

	err = session.ProcessResponse(0, &host.HostResponse{
		Nonce: session.Nonce(),
		Mempool: []*types.DevshardTx{
			injectedStart, injectedTimeout, injectedFinalize, honestAck,
		},
	}, 1)
	require.NoError(t, err)

	pending := session.PendingTxs()
	require.Nil(t, findPendingStart(pending, 2), "host Mempool Start must not be queued")
	require.Nil(t, findPendingTimeout(pending, 1), "host Mempool Timeout must not be queued")
	require.Nil(t, findPendingFinalize(pending), "host Mempool Finalize must not be queued")
	require.NotNil(t, findPendingHeightAck(pending, 1, 0), "host-proposed HeightAck must still queue")
	require.Equal(t, types.PhaseActive, session.StateMachine().SnapshotState().Phase)
}

// TestSendInference_HostInjectedStartIsDropped is the attack-shaped fixture:
// the selected participant returns a valid nonce-1 receipt/Finish and also
// appends an unsigned Start(2) to the same Mempool. The gateway must keep
// Confirm/Finish and drop Start so ordinary Finalize cannot reserve attacker
// cost on nonce 2.
func TestSendInference_HostInjectedStartIsDropped(t *testing.T) {
	session, _, _ := setupSession(t, 3, 8_000_000_000, 100)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	execIdx := 1 // nonce 1 % 3
	wrapHostWithMempoolInjection(session, execIdx, []*types.DevshardTx{
		injectedStartTx(2),
		{Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{InferenceId: 1}}},
		{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}},
	})

	resp, err := session.SendInference(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, findPendingStart(resp.Mempool, 2), "fixture must place Start2 on the host response")
	require.NotNil(t, findPendingFinalize(resp.Mempool), "fixture must place Finalize on the host response")
	require.True(t, HasMsgFinish(resp.Mempool, 1), "honest Finish1 must still be on the response")

	pending := session.PendingTxs()
	require.Nil(t, findPendingStart(pending, 2), "injected Start2 must not enter pending")
	require.Nil(t, findPendingTimeout(pending, 1), "injected Timeout must not enter pending")
	require.Nil(t, findPendingFinalize(pending), "injected Finalize must not enter pending")
	require.NotNil(t, findRecoveryFinish(pending, 1), "host Finish1 must still be queued")
	require.Equal(t, types.PhaseActive, session.StateMachine().SnapshotState().Phase)
	_, hasInjected := session.StateMachine().Inference(2)
	require.False(t, hasInjected)

	require.NoError(t, session.Finalize(ctx))
	requireNoInjectedStartInDiffs(t, session, 1)
	_, hasInjected = session.StateMachine().Inference(2)
	require.False(t, hasInjected, "injected Start must not create an inference record")
	st := session.StateMachine().SnapshotState()
	for slot, hs := range st.HostStats {
		require.Less(t, hs.Cost, uint64(799_999_800),
			"slot %d must not be paid the injected reservation", slot)
	}
}

func TestSendInference_HostInjectedStartDoesNotBlockNextUserInference(t *testing.T) {
	session, _, _ := setupSession(t, 3, 8_000_000_000, 100)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	wrapHostWithMempoolInjection(session, 1, []*types.DevshardTx{injectedStartTx(2)})
	resp, err := session.SendInference(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, findPendingStart(resp.Mempool, 2))
	require.Nil(t, findPendingStart(session.PendingTxs(), 2))

	resp2, err := session.SendInference(ctx, params)
	require.NoError(t, err, "dropped Start2 must not make the next user Start fail compose")
	require.Equal(t, uint64(2), resp2.Nonce)

	rec, ok := session.StateMachine().Inference(2)
	require.True(t, ok, "nonce 2 must be the creator's next inference")
	require.Equal(t, uint64(testutil.TestMaxTokens), rec.MaxTokens, "nonce 2 must use creator params, not the injected reservation")
	require.Equal(t, uint64(100+testutil.TestMaxTokens), rec.ReservedCost)

	foundUserStart2 := false
	for _, d := range session.Diffs() {
		for _, tx := range d.Txs {
			start := tx.GetStartInference()
			if start == nil || start.InferenceId != 2 {
				continue
			}
			foundUserStart2 = true
			require.Equal(t, uint64(testutil.TestMaxTokens), start.MaxTokens)
		}
	}
	require.True(t, foundUserStart2, "creator Start2 must be in a signed Diff")
}

func TestProcessResponse_InjectedStartDoesNotReserveOnFinalize(t *testing.T) {
	session, _, _ := setupSession(t, 3, 8_000_000_000, 100)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	_, err := session.SendInference(ctx, params)
	require.NoError(t, err)

	err = session.ProcessResponse(1, &host.HostResponse{
		Nonce:   session.Nonce(),
		Mempool: []*types.DevshardTx{injectedStartTx(2)},
	}, 1)
	require.NoError(t, err)
	require.Nil(t, findPendingStart(session.PendingTxs(), 2))

	require.NoError(t, session.Finalize(ctx))
	requireNoInjectedStartInDiffs(t, session, 1)
	_, hasInjected := session.StateMachine().Inference(2)
	require.False(t, hasInjected, "injected Start must not create an inference record")
}

// TestProcessResponse_ForgedConfirmDoesNotShadowHonestConfirm covers the dedup
// half of the injection surface. A dedup key is claimed at enqueue time, so a
// host that races in a bogus ConfirmStart used to burn confirm:<id> for the
// lifetime of the session: the tx failed to apply and was discarded, but its
// key survived and silently dropped the executor's real ConfirmStart, leaving
// the inference unconfirmable and settling it at full reserved cost. Keys are
// now retained only for txs that actually applied.
func TestProcessResponse_ForgedConfirmDoesNotShadowHonestConfirm(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 100)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	prepared, err := session.PrepareInference(params)
	require.NoError(t, err)
	nonce := prepared.diff.Nonce
	execIdx := int(nonce % uint64(len(session.clients)))
	execHost := session.clients[execIdx].(*InProcessClient).Host

	_, _, err = execHost.ChallengeReceipt(ctx, nonce, &host.InferencePayload{
		Prompt:      params.Prompt,
		Model:       params.Model,
		InputLength: params.InputLength,
		MaxTokens:   params.MaxTokens,
		StartedAt:   params.StartedAt,
	}, []types.Diff{prepared.diff})
	require.NoError(t, err)

	var honestConfirm *types.DevshardTx
	for _, tx := range execHost.MempoolTxs() {
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == nonce {
			honestConfirm = tx
			break
		}
	}
	require.NotNil(t, honestConfirm, "fixture must produce the executor's ConfirmStart")

	forged := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: nonce,
		ExecutorSig: []byte("not-a-signature"),
		ConfirmedAt: 1000,
	}}}

	// A non-executor host wins the race to confirm:<id>.
	nonExecIdx := (execIdx + 1) % len(session.clients)
	require.NoError(t, session.ProcessResponse(nonExecIdx, &host.HostResponse{
		Nonce:   nonce,
		Mempool: []*types.DevshardTx{forged},
	}, nonce))
	require.NotNil(t, findPendingConfirm(session.PendingTxs(), nonce),
		"fixture must queue the forged ConfirmStart")

	require.NoError(t, session.SendPendingDiff(ctx))
	require.Nil(t, findPendingConfirm(session.PendingTxs(), nonce),
		"forged ConfirmStart must be dropped by best-effort apply")
	rec, ok := session.StateMachine().Inference(nonce)
	require.True(t, ok)
	require.NotEqual(t, types.StatusStarted, rec.Status,
		"forged ConfirmStart must not start the inference")

	require.NoError(t, session.ProcessResponse(execIdx, &host.HostResponse{
		Nonce:   session.Nonce(),
		Mempool: []*types.DevshardTx{honestConfirm},
	}, nonce))
	require.NotNil(t, findPendingConfirm(session.PendingTxs(), nonce),
		"executor ConfirmStart must not be shadowed by the failed forgery")

	require.NoError(t, session.SendPendingDiff(ctx))
	rec, ok = session.StateMachine().Inference(nonce)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, rec.Status,
		"honest ConfirmStart must still be able to start the inference")
}

// TestProcessResponse_AppliedConfirmIsNotRequeued is the counterpart: once a tx
// actually lands in a diff, another host's mempool copy must stay out.
func TestProcessResponse_AppliedConfirmIsNotRequeued(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 100)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	prepared, err := session.PrepareInference(params)
	require.NoError(t, err)
	nonce := prepared.diff.Nonce
	execIdx := int(nonce % uint64(len(session.clients)))
	execHost := session.clients[execIdx].(*InProcessClient).Host

	_, _, err = execHost.ChallengeReceipt(ctx, nonce, &host.InferencePayload{
		Prompt:      params.Prompt,
		Model:       params.Model,
		InputLength: params.InputLength,
		MaxTokens:   params.MaxTokens,
		StartedAt:   params.StartedAt,
	}, []types.Diff{prepared.diff})
	require.NoError(t, err)

	var honestConfirm *types.DevshardTx
	for _, tx := range execHost.MempoolTxs() {
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == nonce {
			honestConfirm = tx
			break
		}
	}
	require.NotNil(t, honestConfirm)

	require.NoError(t, session.ProcessResponse(execIdx, &host.HostResponse{
		Nonce: nonce, Mempool: []*types.DevshardTx{honestConfirm},
	}, nonce))
	require.NoError(t, session.SendPendingDiff(ctx))
	rec, ok := session.StateMachine().Inference(nonce)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, rec.Status)

	require.NoError(t, session.ProcessResponse((execIdx+1)%len(session.clients), &host.HostResponse{
		Nonce: session.Nonce(), Mempool: []*types.DevshardTx{honestConfirm},
	}, nonce))
	require.Nil(t, findPendingConfirm(session.PendingTxs(), nonce),
		"a ConfirmStart already in a diff must not be re-queued from another mempool")
}

// TestProcessResponse_DropsHostTxForUnknownInference pins the admission-time
// existence check. Confirm, Validation and Vote for an id the sequencer does
// not track can never apply, so admitting them only buys a signature recovery
// at compose time -- and, since a failed tx now releases its dedup key, would
// let a host park unbounded made-up ids in pending and replay them every round.
func TestProcessResponse_DropsHostTxForUnknownInference(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 100)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	prepared, err := session.PrepareInference(params)
	require.NoError(t, err)
	nonce := prepared.diff.Nonce

	const unknownID = 4242
	require.NoError(t, session.ProcessResponse(0, &host.HostResponse{
		Nonce: nonce,
		Mempool: []*types.DevshardTx{
			{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
				InferenceId: unknownID, ExecutorSig: []byte("sig"), ConfirmedAt: 1000,
			}}},
			{Tx: &types.DevshardTx_Validation{Validation: &types.MsgValidation{
				InferenceId: unknownID, ValidatorSlot: 0, Valid: true,
			}}},
			{Tx: &types.DevshardTx_ValidationVote{ValidationVote: &types.MsgValidationVote{
				InferenceId: unknownID, VoterSlot: 0,
			}}},
			// Slot-scoped txs carry no inference id and must be unaffected.
			{Tx: &types.DevshardTx_HeightAck{HeightAck: &types.MsgHeightAck{RefNonce: 1, SlotId: 0}}},
		},
	}, nonce))

	pending := session.PendingTxs()
	require.Nil(t, findPendingConfirm(pending, unknownID),
		"ConfirmStart for an unknown inference must not queue")
	for _, tx := range pending {
		if v := tx.GetValidation(); v != nil {
			require.NotEqual(t, uint64(unknownID), v.InferenceId,
				"Validation for an unknown inference must not queue")
		}
		if v := tx.GetValidationVote(); v != nil {
			require.NotEqual(t, uint64(unknownID), v.InferenceId,
				"ValidationVote for an unknown inference must not queue")
		}
	}
	require.NotNil(t, findPendingHeightAck(pending, 1, 0),
		"slot-scoped HeightAck must still queue")
}

func TestProcessResponse_DropsFinishNotSignedByExecutor(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 100)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	prepared, err := session.PrepareInference(params)
	require.NoError(t, err)
	nonce := prepared.diff.Nonce
	execIdx := int(nonce % uint64(len(session.group)))

	unsigned := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{
		FinishInference: &types.MsgFinishInference{
			InferenceId: nonce, ExecutorSlot: uint32(execIdx), EscrowId: "escrow-1",
		},
	}}
	wrongSigner := signedFinishTx(t, hosts, nonce, execIdx, (execIdx+1)%len(hosts))
	wrongSlot := signedFinishTx(t, hosts, nonce, (execIdx+1)%len(hosts), (execIdx+1)%len(hosts))
	unknown := signedFinishTx(t, hosts, 99, execIdx, execIdx)
	valid := signedFinishTx(t, hosts, nonce, execIdx, execIdx)

	require.NoError(t, session.ProcessResponse(execIdx, &host.HostResponse{
		Nonce:   nonce,
		Mempool: []*types.DevshardTx{unsigned, wrongSigner, wrongSlot, unknown},
	}, nonce))
	require.Nil(t, findRecoveryFinish(session.PendingTxs(), nonce), "unsigned or wrong-executor Finish must not queue")
	require.Nil(t, findRecoveryFinish(session.PendingTxs(), 99), "Finish for an unknown inference must not queue")

	require.NoError(t, session.ProcessResponse(execIdx, &host.HostResponse{
		Nonce:   nonce,
		Mempool: []*types.DevshardTx{valid},
	}, nonce))
	require.NotNil(t, findRecoveryFinish(session.PendingTxs(), nonce), "executor-signed Finish must queue")
}

func TestHandleTimeout_RecoveryDropsInjectedStartAndUnsignedFinish(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	prepared, err := session.PrepareInference(params)
	require.NoError(t, err)
	nonce := prepared.diff.Nonce
	execIdx := int(nonce % uint64(len(session.clients)))
	execHost := session.clients[execIdx].(*InProcessClient).Host

	payload := &host.InferencePayload{
		Prompt:      params.Prompt,
		Model:       params.Model,
		InputLength: params.InputLength,
		MaxTokens:   params.MaxTokens,
		StartedAt:   params.StartedAt,
	}
	receipt, _, err := execHost.ChallengeReceipt(ctx, nonce, payload, []types.Diff{prepared.diff})
	require.NoError(t, err)
	require.NotEmpty(t, receipt)

	var confirmTx *types.DevshardTx
	for _, tx := range execHost.MempoolTxs() {
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == nonce {
			confirmTx = tx
			break
		}
	}
	require.NotNil(t, confirmTx)

	unsignedFinish := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{
		FinishInference: &types.MsgFinishInference{
			InferenceId: nonce, ExecutorSlot: uint32(execIdx), EscrowId: "escrow-1",
		},
	}}
	injected := []*types.DevshardTx{confirmTx, unsignedFinish, injectedStartTx(2)}
	for i, c := range session.clients {
		session.clients[i] = &timeoutRecoveryClient{HostClient: c, mempool: injected}
	}

	_, err = session.HandleTimeout(ctx, nonce, time.Unix(0, 0), payload)
	require.NoError(t, err, "valid ConfirmStart recovery must still publish")

	rec, ok := session.StateMachine().SnapshotState().Inferences[nonce]
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, rec.Status)
	_, hasInjected := session.StateMachine().Inference(2)
	require.False(t, hasInjected)
	require.Nil(t, findPendingStart(session.PendingTxs(), 2))
	requireNoInjectedStartInDiffs(t, session, nonce)
	for _, d := range session.Diffs() {
		for _, tx := range d.Txs {
			require.Nil(t, tx.GetFinishInference(), "unsigned recovery Finish must not land in a Diff")
		}
	}
}

func injectedStartTx(inferenceID uint64) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_StartInference{
		StartInference: &types.MsgStartInference{
			InferenceId: inferenceID, Model: "llama", InputLength: 100, MaxTokens: 799_999_800,
		},
	}}
}

type mempoolInjectingClient struct {
	HostClient
	extra []*types.DevshardTx
}

func wrapHostWithMempoolInjection(session *Session, hostIdx int, extra []*types.DevshardTx) {
	session.clients[hostIdx] = &mempoolInjectingClient{HostClient: session.clients[hostIdx], extra: extra}
}

func (c *mempoolInjectingClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	resp, err := c.HostClient.Send(ctx, req, stream, receiptHandler)
	if err != nil || resp == nil {
		return resp, err
	}
	cloned := make([]*types.DevshardTx, len(resp.Mempool), len(resp.Mempool)+len(c.extra))
	copy(cloned, resp.Mempool)
	resp.Mempool = append(cloned, c.extra...)
	return resp, nil
}

func (c *mempoolInjectingClient) GetSignatures(ctx context.Context, nonce uint64) (map[uint32][]byte, error) {
	if f, ok := c.HostClient.(SignatureFetcher); ok {
		return f.GetSignatures(ctx, nonce)
	}
	return nil, fmt.Errorf("inner client has no signatures")
}

func requireNoInjectedStartInDiffs(t *testing.T, session *Session, creatorStartID uint64) {
	t.Helper()
	for _, d := range session.Diffs() {
		for _, tx := range d.Txs {
			start := tx.GetStartInference()
			if start == nil {
				continue
			}
			require.Equal(t, creatorStartID, start.InferenceId, "only the creator-composed Start may land in a Diff")
		}
	}
}

func findPendingStart(txs []*types.DevshardTx, inferenceID uint64) *types.MsgStartInference {
	for _, tx := range txs {
		if start := tx.GetStartInference(); start != nil && start.InferenceId == inferenceID {
			return start
		}
	}
	return nil
}

func findPendingFinalize(txs []*types.DevshardTx) *types.MsgFinalizeRound {
	for _, tx := range txs {
		if fin := tx.GetFinalizeRound(); fin != nil {
			return fin
		}
	}
	return nil
}

func findPendingHeightAck(txs []*types.DevshardTx, refNonce uint64, slotID uint32) *types.MsgHeightAck {
	for _, tx := range txs {
		ack := tx.GetHeightAck()
		if ack != nil && ack.RefNonce == refNonce && ack.SlotId == slotID {
			return ack
		}
	}
	return nil
}

func signedFinishTx(t *testing.T, hosts []*signing.Secp256k1Signer, nonce uint64, executorSlot, signerIdx int) *types.DevshardTx {
	t.Helper()
	msg := &types.MsgFinishInference{
		InferenceId:  nonce,
		ResponseHash: []byte("hash"),
		InputTokens:  80,
		OutputTokens: 40,
		ExecutorSlot: uint32(executorSlot),
		EscrowId:     "escrow-1",
	}
	msg.ProposerSig = testutil.SignProposerTx(t, hosts[signerIdx], msg)
	return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}
}

func TestProcessResponse_NilReturnsNamedError(t *testing.T) {
	session, _, _ := setupSession(t, 2, 100000, 100)
	err := session.ProcessResponse(0, nil, 1)
	require.ErrorIs(t, err, ErrNilHostResponse)
	require.Equal(t, uint64(0), session.SnapshotHeightSync().Overlap.Total)
}

func TestProcessResponse_FailedVerifySkipsContactAndOverlap(t *testing.T) {
	session, _, _ := setupSessionWithOptions(t, 2, 100000, 100, WithHeightSyncCadence(10, 2))
	err := session.ProcessResponse(0, &host.HostResponse{
		Nonce:     99,
		StateHash: []byte{0xde, 0xad},
	}, 1)
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrStateHashMismatch)
	require.True(t, session.lastContact[0].IsZero(), "failed verify must not refresh contact")
	require.Equal(t, uint64(0), session.SnapshotHeightSync().Overlap.Total,
		"failed verify must not count overlap")
}

func TestCollectTimeoutVotes_WeightEarlyExit(t *testing.T) {
	// 4 signers with slots [1, 1, 3, 1] (total 6 slots).
	// VoteThreshold = 6/2 = 3. Need >3 weighted accept votes.
	// Signer[2] (weight=3) + any other (weight=1) = 4 > 3. Should early-exit with 2 votes.
	signers := make([]*signing.Secp256k1Signer, 4)
	for i := range signers {
		signers[i] = testutil.MustGenerateKey(t)
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeMultiSlotGroup(signers, []int{1, 1, 3, 1})
	numSlots := len(group)
	config := types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		VoteThreshold:    uint32(numSlots) / 2, // 6/2 = 3
	}
	verifier := signing.NewSecp256k1Verifier()

	// Build per-slot hosts. Each slot gets a host with the correct signer.
	clients := make([]HostClient, numSlots)
	for i, slot := range group {
		var slotSigner *signing.Secp256k1Signer
		for _, s := range signers {
			if s.Address() == slot.ValidatorAddress {
				slotSigner = s
				break
			}
		}
		sm := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, userKey.Address(), verifier)
		engine := stub.NewInferenceEngine()
		h, err := host.NewHost(sm, slotSigner, engine, "escrow-1", group, nil, host.WithGrace(100))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, userKey.Address(), verifier)
	session, err := NewSession(userSM, userKey, "escrow-1", group, clients, verifier)
	require.NoError(t, err)

	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	_, err = session.SendInference(ctx, params)
	require.NoError(t, err)

	// Executor = group[1%6].SlotID = 1 (signer[1]).
	// Build mock verifiers for non-executor slots. Each mock signs with its slot's signer.
	executorIdx := int(1 % uint64(numSlots))
	verifiers := make(map[int]TimeoutVerifier)
	for i, slot := range group {
		if i == executorIdx {
			continue
		}
		var slotSigner *signing.Secp256k1Signer
		for _, s := range signers {
			if s.Address() == slot.ValidatorAddress {
				slotSigner = s
				break
			}
		}
		verifiers[i] = &mockTimeoutVerifier{accept: true, signer: slotSigner, group: group, slotIdx: i}
	}

	votes, _, _, err := session.CollectTimeoutVotes(ctx, 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, &host.InferencePayload{
		Prompt:      testutil.TestPrompt,
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   testutil.TestMaxTokens,
		StartedAt:   1000,
	}, verifiers, nil)
	require.NoError(t, err)

	// Compute total weight of returned votes.
	var totalWeight uint32
	for _, v := range votes {
		addr := userSM.SlotAddress(v.VoterSlot)
		totalWeight += userSM.AddressSlotCount(addr)
	}
	require.True(t, totalWeight > config.VoteThreshold,
		"accumulated weight %d should exceed threshold %d", totalWeight, config.VoteThreshold)
}

type mockTimeoutVerifier struct {
	accept      bool
	signer      *signing.Secp256k1Signer
	group       []types.SlotAssignment
	slotIdx     int
	escrowID    string // defaults to "escrow-1" when empty
	mempool     []*types.DevshardTx
	rejectCause string
	delay       time.Duration
	onVerify    func()
}

func (m *mockTimeoutVerifier) VerifyTimeout(_ context.Context, inferenceID uint64, reason types.TimeoutReason, _ *host.InferencePayload, _ []types.Diff, _ host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	if m.onVerify != nil {
		m.onVerify()
	}
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	if !m.accept {
		return false, nil, 0, m.mempool, m.rejectCause, nil
	}
	eid := m.escrowID
	if eid == "" {
		eid = "escrow-1"
	}
	voterSlot := m.group[m.slotIdx].SlotID
	content := &types.TimeoutVoteContent{
		EscrowId:    eid,
		InferenceId: inferenceID,
		Reason:      reason,
		Accept:      true,
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(content)
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	sig, err := m.signer.Sign(data)
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	return true, sig, voterSlot, nil, "", nil
}

func (m *mockTimeoutVerifier) VerifyErrorMiss(_ context.Context, inferenceID uint64, _ []types.Diff, artifacts host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	if m.onVerify != nil {
		m.onVerify()
	}
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	if !m.accept {
		return false, nil, 0, m.mempool, m.rejectCause, nil
	}
	eid := m.escrowID
	if eid == "" {
		eid = "escrow-1"
	}
	voterSlot := m.group[m.slotIdx].SlotID
	var hash []byte
	if len(artifacts.ResponsePayload) > 0 {
		sum := sha256.Sum256(artifacts.ResponsePayload)
		hash = sum[:]
	}
	content := &types.ErrorMissVoteContent{
		EscrowId:     eid,
		InferenceId:  inferenceID,
		Accept:       true,
		ResponseHash: hash,
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(content)
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	sig, err := m.signer.Sign(data)
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	return true, sig, voterSlot, nil, "", nil
}

// concurrencyMockVerifier is a TimeoutVerifier that records concurrency
// observed at each verifier slot. It blocks inside VerifyTimeout until the
// caller closes/sends on `release`, allowing tests to deterministically
// observe how many calls are simultaneously in-flight against the same
// verifier address. The same shared maps are passed to every verifier so
// the observation is global across the whole CollectTimeoutVotes fan-out.
type concurrencyMockVerifier struct {
	slotIdx       int
	group         []types.SlotAssignment
	signer        *signing.Secp256k1Signer
	perSlotActive map[int]*atomic.Int32 // slotIdx -> currently in-flight VerifyTimeout count
	perSlotMax    map[int]*atomic.Int32 // slotIdx -> peak observed concurrency
	totalEntered  *atomic.Int32         // total calls that have entered the critical section
	enteredCh     chan int              // optional: receives slotIdx on every entry
	release       <-chan struct{}       // VerifyTimeout returns when this is closed
}

func (m *concurrencyMockVerifier) VerifyTimeout(ctx context.Context, inferenceID uint64, reason types.TimeoutReason, _ *host.InferencePayload, _ []types.Diff, _ host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	cur := m.perSlotActive[m.slotIdx].Add(1)
	defer m.perSlotActive[m.slotIdx].Add(-1)
	if m.totalEntered != nil {
		m.totalEntered.Add(1)
	}
	for {
		old := m.perSlotMax[m.slotIdx].Load()
		if cur <= old {
			break
		}
		if m.perSlotMax[m.slotIdx].CompareAndSwap(old, cur) {
			break
		}
	}
	if m.enteredCh != nil {
		select {
		case m.enteredCh <- m.slotIdx:
		default:
		}
	}
	if m.release != nil {
		select {
		case <-m.release:
		case <-ctx.Done():
			return false, nil, 0, nil, "", ctx.Err()
		}
	}
	voterSlot := m.group[m.slotIdx].SlotID
	content := &types.TimeoutVoteContent{
		EscrowId:    "escrow-1",
		InferenceId: inferenceID,
		Reason:      reason,
		Accept:      true,
	}
	data, err := proto.Marshal(content)
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	sig, err := m.signer.Sign(data)
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	return true, sig, voterSlot, nil, "", nil
}

func (m *concurrencyMockVerifier) VerifyErrorMiss(ctx context.Context, inferenceID uint64, diffs []types.Diff, artifacts host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	return false, nil, 0, nil, "", nil
}

// signerForSlot finds the signer whose address matches the slot's validator.
func signerForSlot(t *testing.T, signers []*signing.Secp256k1Signer, slot types.SlotAssignment) *signing.Secp256k1Signer {
	t.Helper()
	for _, s := range signers {
		if s.Address() == slot.ValidatorAddress {
			return s
		}
	}
	t.Fatalf("no signer found for slot %s", slot.ValidatorAddress)
	return nil
}

// TestCollectTimeoutVotes_SerializesPerVerifier verifies that two concurrent
// CollectTimeoutVotes calls targeting the same verifier set never make more
// than MaxConcurrentVerifierRPCs simultaneous VerifyTimeout calls against any
// single verifier — even though within a single call different verifiers are
// still hit in parallel.
func TestCollectTimeoutVotes_SerializesPerVerifier(t *testing.T) {
	saved := MaxConcurrentVerifierRPCs
	MaxConcurrentVerifierRPCs = 1
	t.Cleanup(func() { MaxConcurrentVerifierRPCs = saved })

	session, hosts, _ := setupSessionWithOptions(t, 3, 100000, 10, WithVerifierQueue(newVerifierHostQueue()))
	ctx := context.Background()

	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	_, err := session.SendInference(ctx, params)
	require.NoError(t, err)

	nonce := uint64(1)
	executorIdx := int(nonce % uint64(len(session.group)))

	perSlotActive := make(map[int]*atomic.Int32)
	perSlotMax := make(map[int]*atomic.Int32)
	for i := range session.group {
		perSlotActive[i] = &atomic.Int32{}
		perSlotMax[i] = &atomic.Int32{}
	}

	var totalEntered atomic.Int32
	release := make(chan struct{})

	buildVerifiers := func() map[int]TimeoutVerifier {
		v := make(map[int]TimeoutVerifier)
		for i, slot := range session.group {
			if i == executorIdx {
				continue
			}
			v[i] = &concurrencyMockVerifier{
				slotIdx:       i,
				group:         session.group,
				signer:        signerForSlot(t, hosts, slot),
				perSlotActive: perSlotActive,
				perSlotMax:    perSlotMax,
				totalEntered:  &totalEntered,
				release:       release,
			}
		}
		return v
	}

	payload := &host.InferencePayload{
		Prompt: testutil.TestPrompt, Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	type collectResult struct {
		votes []*types.TimeoutVote
		err   error
	}
	resultsCh := make(chan collectResult, 2)

	for i := 0; i < 2; i++ {
		go func() {
			votes, _, _, err := session.CollectTimeoutVotes(ctx, nonce, types.TimeoutReason_TIMEOUT_REASON_REFUSED, payload, buildVerifiers(), nil)
			resultsCh <- collectResult{votes: votes, err: err}
		}()
	}

	// With 3 hosts, executor=slot 1, there are 2 verifier slots (0 and 2).
	// Two concurrent calls => 4 VerifyTimeout invocations total (2 per
	// verifier). MaxConcurrentVerifierRPCs=1 means at any instant at most
	// 1 call per verifier should be active. Different verifiers can run
	// in parallel so total in-flight may briefly equal len(verifiers)=2.
	require.Eventually(t, func() bool {
		return totalEntered.Load() >= 2
	}, 2*time.Second, 5*time.Millisecond, "at least one call per verifier should reach the critical section")

	// Give the runtime a brief slice to (incorrectly) start a second call
	// against the same verifier if the queue is broken.
	time.Sleep(50 * time.Millisecond)

	for slotIdx, peak := range perSlotMax {
		require.LessOrEqualf(t, peak.Load(), int32(1),
			"verifier slot %d observed %d concurrent VerifyTimeout calls; expected ≤1",
			slotIdx, peak.Load())
	}

	close(release)

	for i := 0; i < 2; i++ {
		select {
		case r := <-resultsCh:
			require.NoError(t, r.err)
		case <-time.After(2 * time.Second):
			t.Fatal("CollectTimeoutVotes did not return after release")
		}
	}

	for slotIdx, peak := range perSlotMax {
		require.LessOrEqualf(t, peak.Load(), int32(1),
			"verifier slot %d final peak concurrency was %d; expected ≤1",
			slotIdx, peak.Load())
	}
}

// TestCollectTimeoutVotes_DifferentVerifiersRunInParallel confirms the queue
// is per-verifier: a single CollectTimeoutVotes call still hits N different
// verifiers concurrently. Without parallelism here, the queue would be a
// global serializer instead of per-host.
func TestCollectTimeoutVotes_DifferentVerifiersRunInParallel(t *testing.T) {
	saved := MaxConcurrentVerifierRPCs
	MaxConcurrentVerifierRPCs = 1
	t.Cleanup(func() { MaxConcurrentVerifierRPCs = saved })

	session, hosts, _ := setupSessionWithOptions(t, 3, 100000, 10, WithVerifierQueue(newVerifierHostQueue()))
	ctx := context.Background()

	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	_, err := session.SendInference(ctx, params)
	require.NoError(t, err)

	nonce := uint64(1)
	executorIdx := int(nonce % uint64(len(session.group)))
	expectedVerifiers := len(session.group) - 1
	require.Equal(t, 2, expectedVerifiers, "test assumes 3-host group")

	perSlotActive := make(map[int]*atomic.Int32)
	perSlotMax := make(map[int]*atomic.Int32)
	for i := range session.group {
		perSlotActive[i] = &atomic.Int32{}
		perSlotMax[i] = &atomic.Int32{}
	}

	var totalEntered atomic.Int32
	release := make(chan struct{})

	verifiers := make(map[int]TimeoutVerifier)
	for i, slot := range session.group {
		if i == executorIdx {
			continue
		}
		verifiers[i] = &concurrencyMockVerifier{
			slotIdx:       i,
			group:         session.group,
			signer:        signerForSlot(t, hosts, slot),
			perSlotActive: perSlotActive,
			perSlotMax:    perSlotMax,
			totalEntered:  &totalEntered,
			release:       release,
		}
	}

	payload := &host.InferencePayload{
		Prompt: testutil.TestPrompt, Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	done := make(chan error, 1)
	go func() {
		_, _, _, err := session.CollectTimeoutVotes(ctx, nonce, types.TimeoutReason_TIMEOUT_REASON_REFUSED, payload, verifiers, nil)
		done <- err
	}()

	require.Eventuallyf(t, func() bool {
		return totalEntered.Load() == int32(expectedVerifiers)
	}, 2*time.Second, 5*time.Millisecond,
		"expected %d different verifiers to enter VerifyTimeout concurrently, got %d",
		expectedVerifiers, totalEntered.Load())

	close(release)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("CollectTimeoutVotes did not return after release")
	}
}

// TestCollectTimeoutVotes_WaitTimeoutDropsStaleGoroutines verifies that a
// goroutine which cannot acquire its verifier's slot within
// VerifierQueueWaitTimeout exits cleanly with an error instead of waiting
// forever, and never fires VerifyTimeout. This is the safety net that
// bounds goroutine growth when a verifier hangs.
func TestCollectTimeoutVotes_WaitTimeoutDropsStaleGoroutines(t *testing.T) {
	savedCap := MaxConcurrentVerifierRPCs
	MaxConcurrentVerifierRPCs = 1
	savedWait := VerifierQueueWaitTimeout
	VerifierQueueWaitTimeout = 50 * time.Millisecond
	t.Cleanup(func() {
		MaxConcurrentVerifierRPCs = savedCap
		VerifierQueueWaitTimeout = savedWait
	})

	session, hosts, _ := setupSessionWithOptions(t, 3, 100000, 10, WithVerifierQueue(newVerifierHostQueue()))
	ctx := context.Background()

	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	_, err := session.SendInference(ctx, params)
	require.NoError(t, err)

	nonce := uint64(1)
	executorIdx := int(nonce % uint64(len(session.group)))

	perSlotActive := make(map[int]*atomic.Int32)
	perSlotMax := make(map[int]*atomic.Int32)
	for i := range session.group {
		perSlotActive[i] = &atomic.Int32{}
		perSlotMax[i] = &atomic.Int32{}
	}

	// First call's verifiers never return — they hold their slots until
	// releaseFirst is closed. This simulates hung verifiers and forces
	// the second call's goroutines to wait on the queue.
	releaseFirst := make(chan struct{})
	defer close(releaseFirst)

	var firstEntered atomic.Int32
	firstVerifiers := make(map[int]TimeoutVerifier)
	for i, slot := range session.group {
		if i == executorIdx {
			continue
		}
		firstVerifiers[i] = &concurrencyMockVerifier{
			slotIdx:       i,
			group:         session.group,
			signer:        signerForSlot(t, hosts, slot),
			perSlotActive: perSlotActive,
			perSlotMax:    perSlotMax,
			totalEntered:  &firstEntered,
			release:       releaseFirst,
		}
	}

	// Second call's verifiers should NEVER be entered — the queue wait
	// should expire first.
	var secondEntered atomic.Int32
	secondVerifiers := make(map[int]TimeoutVerifier)
	for i, slot := range session.group {
		if i == executorIdx {
			continue
		}
		secondVerifiers[i] = &concurrencyMockVerifier{
			slotIdx:       i,
			group:         session.group,
			signer:        signerForSlot(t, hosts, slot),
			perSlotActive: perSlotActive,
			perSlotMax:    perSlotMax,
			totalEntered:  &secondEntered,
			// no release: would block forever if reached, but we
			// expect the wait-timeout to stop it before entry.
		}
	}

	payload := &host.InferencePayload{
		Prompt: testutil.TestPrompt, Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	// Launch the blocking first call so all verifier slots are occupied.
	firstDone := make(chan struct{})
	go func() {
		_, _, _, _ = session.CollectTimeoutVotes(ctx, nonce, types.TimeoutReason_TIMEOUT_REASON_REFUSED, payload, firstVerifiers, nil)
		close(firstDone)
	}()

	// Wait until the first call has actually grabbed every verifier slot.
	expectedVerifiers := int32(len(session.group) - 1)
	require.Eventually(t, func() bool {
		return firstEntered.Load() == expectedVerifiers
	}, time.Second, 5*time.Millisecond, "first call should occupy every verifier slot")

	// Now fire the second call. Its goroutines should all time out on the
	// queue (50ms) and return without calling VerifyTimeout.
	start := time.Now()
	votes, _, _, err := session.CollectTimeoutVotes(ctx, nonce, types.TimeoutReason_TIMEOUT_REASON_REFUSED, payload, secondVerifiers, nil)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Empty(t, votes, "stale goroutines must not produce votes")
	require.Zero(t, secondEntered.Load(), "second call's VerifyTimeout must never be invoked")

	// The whole second call must return promptly — bounded by the wait
	// timeout, not by the first call's blocked verifiers.
	require.Less(t, elapsed, 2*time.Second,
		"second CollectTimeoutVotes should return within ~VerifierQueueWaitTimeout; took %v", elapsed)
}

// TestCollectTimeoutVotes_DepthGreaterThanOne raises the per-verifier cap
// to 2 and confirms two concurrent CollectTimeoutVotes calls can both be
// inside VerifyTimeout on the same verifier at the same time.
func TestCollectTimeoutVotes_DepthGreaterThanOne(t *testing.T) {
	saved := MaxConcurrentVerifierRPCs
	MaxConcurrentVerifierRPCs = 2
	t.Cleanup(func() { MaxConcurrentVerifierRPCs = saved })

	session, hosts, _ := setupSessionWithOptions(t, 3, 100000, 10, WithVerifierQueue(newVerifierHostQueue()))
	ctx := context.Background()

	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	_, err := session.SendInference(ctx, params)
	require.NoError(t, err)

	nonce := uint64(1)
	executorIdx := int(nonce % uint64(len(session.group)))

	perSlotActive := make(map[int]*atomic.Int32)
	perSlotMax := make(map[int]*atomic.Int32)
	verifierSlots := make([]int, 0, len(session.group)-1)
	for i := range session.group {
		perSlotActive[i] = &atomic.Int32{}
		perSlotMax[i] = &atomic.Int32{}
		if i != executorIdx {
			verifierSlots = append(verifierSlots, i)
		}
	}

	var totalEntered atomic.Int32
	release := make(chan struct{})

	buildVerifiers := func() map[int]TimeoutVerifier {
		v := make(map[int]TimeoutVerifier)
		for i, slot := range session.group {
			if i == executorIdx {
				continue
			}
			v[i] = &concurrencyMockVerifier{
				slotIdx:       i,
				group:         session.group,
				signer:        signerForSlot(t, hosts, slot),
				perSlotActive: perSlotActive,
				perSlotMax:    perSlotMax,
				totalEntered:  &totalEntered,
				release:       release,
			}
		}
		return v
	}

	payload := &host.InferencePayload{
		Prompt: testutil.TestPrompt, Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _, err := session.CollectTimeoutVotes(ctx, nonce, types.TimeoutReason_TIMEOUT_REASON_REFUSED, payload, buildVerifiers(), nil)
			require.NoError(t, err)
		}()
	}

	// Both calls together fire 4 VerifyTimeout invocations: 2 per verifier
	// slot. With cap=2 we expect every verifier slot to reach 2 concurrent
	// calls. Only count slots that actually have a verifier (i.e. exclude
	// the executor slot, which has no entry in the verifiers map).
	require.Eventually(t, func() bool {
		for _, slotIdx := range verifierSlots {
			if perSlotMax[slotIdx].Load() < 2 {
				return false
			}
		}
		return true
	}, 2*time.Second, 5*time.Millisecond,
		"with cap=2, every verifier should observe 2 concurrent VerifyTimeout calls")

	close(release)
	wg.Wait()
}

// Fixed private keys for reproducible finalize/settlement runs.
var settlementFixedKeys = []string{
	"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
}

func TestUser_Finalize_SeedRevealAndSettlement(t *testing.T) {
	numHosts := 3
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustSignerFromHex(t, settlementFixedKeys[i])
	}
	userKey := testutil.MustSignerFromHex(t, "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	group := testutil.MakeGroup(hosts)
	config := types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		VoteThreshold:    uint32(numHosts) / 2,
		ValidationRate:   10000, // 100%
	}
	verifier := signing.NewSecp256k1Verifier()

	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, userKey.Address(), verifier)
		engine := stub.NewInferenceEngine()
		h, err := host.NewHost(sm, hosts[i], engine, "escrow-1", group, nil, host.WithGrace(100))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, userKey.Address(), verifier)
	session, err := NewSession(userSM, userKey, "escrow-1", group, clients, verifier)
	require.NoError(t, err)

	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	// Send 3 inferences (one per host via round-robin).
	for i := 0; i < numHosts; i++ {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	err = session.Finalize(ctx)
	require.NoError(t, err)

	st := session.StateMachine().SnapshotState()

	for slotID, hs := range st.HostStats {
		require.Zero(t, hs.RequiredValidations, "slot %d required validations must stay zero", slotID)
		require.Zero(t, hs.CompletedValidations, "slot %d completed validations must stay zero", slotID)
	}

	// Build settlement and verify via VerifySettlement.
	finalNonce := session.Nonce()
	sigs := session.Signatures()
	latestSigs, ok := sigs[finalNonce]
	require.True(t, ok, "should have signatures for final nonce")

	payload, err := state.BuildSettlement("escrow-1", st, latestSigs, finalNonce)
	require.NoError(t, err)

	root, err := state.VerifySettlement(*payload, group, verifier, nil)
	require.NoError(t, err)
	require.Len(t, root, 32)
}

// setupDeadHostSession creates a session where only aliveCount hosts work.
// Slots [0, aliveCount) are real InProcessClients; the rest are ErrorClients.
func setupDeadHostSession(t *testing.T, numHosts, aliveCount int, balance uint64, grace uint64) *Session {
	t.Helper()
	hostSigners := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	clients := make([]HostClient, numHosts)
	for i := range clients {
		if i < aliveCount {
			sm := statetest.MustStateMachine(t, "escrow-1", config, group, balance, userKey.Address(), verifier)
			h, err := host.NewHost(sm, hostSigners[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(grace))
			require.NoError(t, err)
			clients[i] = &InProcessClient{Host: h}
		} else {
			clients[i] = &ErrorClient{Err: fmt.Errorf("host %d dead", i)}
		}
	}

	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, balance, userKey.Address(), verifier)
	session, err := NewSession(userSM, userKey, "escrow-1", group, clients, verifier,
		WithCollectRetry(0, 0, 5*time.Second))
	require.NoError(t, err)
	return session
}

func TestFinalize_DoubleCall_AfterSuccess(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 100)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	for i := 0; i < 3; i++ {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	err := session.Finalize(ctx)
	require.NoError(t, err)

	nonce1 := session.Nonce()
	diffs1 := len(session.Diffs())
	st1 := session.StateMachine().SnapshotState()
	require.Equal(t, types.PhaseSettlement, st1.Phase)

	err = session.Finalize(ctx)
	require.NoError(t, err)

	require.Equal(t, nonce1, session.Nonce(), "nonce must not advance on second call")
	require.Equal(t, diffs1, len(session.Diffs()), "no new diffs on second call")
	require.Equal(t, types.PhaseSettlement, session.StateMachine().SnapshotState().Phase)

	sigs := session.Signatures()
	latestSigs := sigs[nonce1]
	payload, err := state.BuildSettlement("escrow-1", st1, latestSigs, nonce1)
	require.NoError(t, err)
	verifier := signing.NewSecp256k1Verifier()
	root, err := state.VerifySettlement(*payload, session.StateMachine().SnapshotState().Group, verifier, nil)
	require.NoError(t, err)
	require.Len(t, root, 32)
}

func TestFinalize_DoubleCall_InsufficientQuorum(t *testing.T) {
	// 5 hosts, only slot 0 alive. Threshold = 2*5/3+1 = 4. Only 1 signature possible.
	session := setupDeadHostSession(t, 5, 1, 100000, 100)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	for i := 0; i < 5; i++ {
		session.SendInference(ctx, params) //nolint:errcheck
	}

	err := session.Finalize(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "insufficient signatures")

	nonce1 := session.Nonce()
	diffs1 := len(session.Diffs())

	require.Equal(t, types.PhaseSettlement, session.StateMachine().SnapshotState().Phase)

	err = session.Finalize(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "insufficient signatures")

	require.Equal(t, nonce1, session.Nonce(), "nonce must not advance on second call")
	require.Equal(t, diffs1, len(session.Diffs()), "no new diffs on second call")
}

// After snapshot-only recovery, diffs and in-memory signatures are empty while
// phase is already Settlement. Re-running Finalize must collect from hosts
// using the live state-root fallback in fetchSignature.
func TestFinalize_SettlementRerun_EmptyDiffsCollectsFromHosts(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 100)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	for i := 0; i < 3; i++ {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}
	require.NoError(t, session.Finalize(ctx))
	require.Equal(t, types.PhaseSettlement, session.StateMachine().SnapshotState().Phase)
	require.True(t, session.HasQuorumAt(session.Nonce()))

	finalNonce := session.Nonce()
	// Sign over the ORIGINAL final-diff post-state-root (what a real host signed
	// at finalize time), captured before wiping diffs. Verifying these against
	// the post-recovery live ComputeStateRoot proves the two roots are equal.
	diffs := session.Diffs()
	originalRoot := append([]byte(nil), diffs[len(diffs)-1].PostStateRoot...)
	require.NotEmpty(t, originalRoot)

	// Default in-process test hosts have no signature store (GET fails). Inject
	// SignatureFetcher clients that serve valid cold-key signatures over the
	// original root — the production HTTP path after hosts already signed.
	fetchers := make([]HostClient, len(hosts))
	for i, hostSigner := range hosts {
		sigData, err := proto.Marshal(&types.StateSignatureContent{
			StateRoot: originalRoot, EscrowId: session.escrowID, Nonce: finalNonce,
		})
		require.NoError(t, err)
		stateSig, err := hostSigner.Sign(sigData)
		require.NoError(t, err)
		fetchers[i] = &fakeFetcher{sigs: map[uint32][]byte{uint32(i): stateSig}}
	}

	session.mu.Lock()
	session.diffs = nil
	session.signatures = make(map[uint64]map[uint32][]byte)
	session.clients = fetchers
	session.mu.Unlock()
	require.False(t, session.HasQuorumAt(finalNonce))

	require.NoError(t, session.Finalize(ctx), "Finalize must re-collect quorum with empty diffs")
	require.True(t, session.HasQuorumAt(finalNonce))
	require.Equal(t, finalNonce, session.Nonce())
}

func TestFinalize_SignatureStatus(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 100)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	for i := 0; i < 3; i++ {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	err := session.Finalize(ctx)
	require.NoError(t, err)

	entries, highestQuorum, hasAny := session.SignatureStatus()
	require.True(t, hasAny)
	require.Equal(t, session.Nonce(), highestQuorum)

	finalNonce := session.Nonce()
	var finalEntry *SignatureStatusEntry
	for i := range entries {
		if entries[i].Nonce == finalNonce {
			finalEntry = &entries[i]
			break
		}
	}
	require.NotNil(t, finalEntry, "must have entry for final nonce")
	require.True(t, finalEntry.HasQuorum)
	require.Equal(t, uint32(3), finalEntry.SigWeight)
	require.Equal(t, uint32(3), finalEntry.Total)
}

func TestFinalize_SignatureStatus_InsufficientQuorum(t *testing.T) {
	// 5 hosts, only slot 0 alive. Threshold = 4.
	session := setupDeadHostSession(t, 5, 1, 100000, 100)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	for i := 0; i < 5; i++ {
		session.SendInference(ctx, params) //nolint:errcheck
	}

	err := session.Finalize(ctx)
	require.Error(t, err)

	entries, _, _ := session.SignatureStatus()
	finalNonce := session.Nonce()

	var finalEntry *SignatureStatusEntry
	for i := range entries {
		if entries[i].Nonce == finalNonce {
			finalEntry = &entries[i]
			break
		}
	}

	if finalEntry != nil {
		require.False(t, finalEntry.HasQuorum)
		require.Less(t, finalEntry.SigWeight, uint32(4))
	}
}

type timeoutRecoveryClient struct {
	HostClient
	mempool []*types.DevshardTx
}

func (c *timeoutRecoveryClient) VerifyTimeout(_ context.Context, _ uint64, _ types.TimeoutReason, _ *host.InferencePayload, _ []types.Diff, _ host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	return false, nil, 0, c.mempool, "", nil
}

func (c *timeoutRecoveryClient) VerifyErrorMiss(_ context.Context, _ uint64, _ []types.Diff, _ host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	return false, nil, 0, c.mempool, "", nil
}

type timeoutVoteClient struct {
	HostClient
	*mockTimeoutVerifier
}

func TestCollectTimeoutVotes_CollectsRejectMempool(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 1,
		ExecutorSig: []byte("executor-receipt"),
		ConfirmedAt: 1000,
	}}}

	other := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 999,
		ExecutorSig: []byte("other"),
		ConfirmedAt: 1,
	}}}
	empty := &types.DevshardTx{}
	mixed := []*types.DevshardTx{empty, other, confirm}

	verifiers := map[int]TimeoutVerifier{
		0: &mockTimeoutVerifier{accept: false, mempool: mixed},
		2: &mockTimeoutVerifier{accept: false, mempool: mixed},
	}

	votes, recovery, _, err := session.CollectTimeoutVotes(context.Background(), 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, &host.InferencePayload{
		Prompt:      testutil.TestPrompt,
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   testutil.TestMaxTokens,
		StartedAt:   1000,
	}, verifiers, nil)
	require.NoError(t, err)
	require.Empty(t, votes)

	var got *types.MsgConfirmStart
	for _, tx := range recovery {
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == 1 {
			got = cs
			break
		}
	}
	require.NotNil(t, got, "reject votes must surface recovery ConfirmStart")
	require.Equal(t, []byte("executor-receipt"), got.ExecutorSig)
	require.Len(t, recovery, 1, "recovery must drop empty txs and ConfirmStart for other inferences")
	require.Equal(t, uint64(1), recovery[0].GetConfirmStart().InferenceId)
}

func TestCollectTimeoutVotes_DeduplicatesRejectRecoveryTxs(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 1,
		ExecutorSig: []byte("executor-receipt"),
		ConfirmedAt: 1000,
	}}}
	finish := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{
		InferenceId:  1,
		ResponseHash: []byte("done"),
	}}}

	verifiers := map[int]TimeoutVerifier{
		0: &mockTimeoutVerifier{accept: false, mempool: []*types.DevshardTx{confirm, finish}},
		2: &mockTimeoutVerifier{accept: false, mempool: []*types.DevshardTx{confirm, finish}},
	}

	votes, recovery, _, err := session.CollectTimeoutVotes(context.Background(), 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, &host.InferencePayload{
		Prompt:      testutil.TestPrompt,
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   testutil.TestMaxTokens,
		StartedAt:   1000,
	}, verifiers, nil)
	require.NoError(t, err)
	require.Empty(t, votes)
	require.Len(t, recovery, 2, "duplicate recovery txs from multiple reject votes must collapse by tx type and inference")
	require.Equal(t, 1, countRecoveryConfirmStart(recovery, 1))
	require.Equal(t, 1, countRecoveryFinish(recovery, 1))
}

func TestCollectTimeoutVotes_DropsMalformedOrNilRecoveryTxs(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	unrelatedConfirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 99,
		ExecutorSig: []byte("other-receipt"),
		ConfirmedAt: 1000,
	}}}
	unrelatedFinish := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{
		InferenceId:  99,
		ResponseHash: []byte("other"),
	}}}
	timeoutTx := &types.DevshardTx{Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{
		InferenceId: 1,
		Reason:      types.TimeoutReason_TIMEOUT_REASON_REFUSED,
	}}}
	malformed := []*types.DevshardTx{nil, {}, unrelatedConfirm, unrelatedFinish, timeoutTx}

	verifiers := map[int]TimeoutVerifier{
		0: &mockTimeoutVerifier{accept: false, mempool: malformed},
		2: &mockTimeoutVerifier{accept: false, mempool: malformed},
	}

	votes, recovery, _, err := session.CollectTimeoutVotes(context.Background(), 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, &host.InferencePayload{
		Prompt:      testutil.TestPrompt,
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   testutil.TestMaxTokens,
		StartedAt:   1000,
	}, verifiers, nil)
	require.NoError(t, err)
	require.Empty(t, votes)
	require.Empty(t, recovery, "nil, empty, user-proposed, and unrelated txs must not become recovery")
}

func TestHandleTimeout_RefusedReject_PublishesConfirmStart(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	prepared, err := session.PrepareInference(params)
	require.NoError(t, err)

	payload := &host.InferencePayload{
		Prompt:      params.Prompt,
		Model:       params.Model,
		InputLength: params.InputLength,
		MaxTokens:   params.MaxTokens,
		StartedAt:   params.StartedAt,
	}

	execIdx := int(prepared.diff.Nonce % uint64(len(session.clients)))
	execHost := session.clients[execIdx].(*InProcessClient).Host
	receipt, _, err := execHost.ChallengeReceipt(ctx, prepared.diff.Nonce, payload, []types.Diff{prepared.diff})
	require.NoError(t, err)
	require.NotEmpty(t, receipt)

	var confirmTx *types.DevshardTx
	for _, tx := range execHost.MempoolTxs() {
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == prepared.diff.Nonce {
			confirmTx = tx
			break
		}
	}
	require.NotNil(t, confirmTx, "executor challenge must queue MsgConfirmStart")

	for i, c := range session.clients {
		session.clients[i] = &timeoutRecoveryClient{HostClient: c, mempool: []*types.DevshardTx{confirmTx}}
	}

	_, err = session.HandleTimeout(ctx, prepared.diff.Nonce, time.Unix(0, 0), payload)
	require.NoError(t, err, "recovery publish must not be treated as a timeout failure")

	rec, ok := session.StateMachine().SnapshotState().Inferences[prepared.diff.Nonce]
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, rec.Status)

	peerIdx := int((prepared.diff.Nonce + 1) % uint64(len(session.clients)))
	peerHost := session.clients[peerIdx].(*timeoutRecoveryClient).HostClient.(*InProcessClient).Host
	peerRec, ok := peerHost.SnapshotState().Inferences[prepared.diff.Nonce]
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, peerRec.Status, "peer SM must apply ConfirmStart from the recovery diff")
}

func TestHandleTimeout_ExecutionTimeoutIgnoresConfirmStartRecovery(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	prepared, err := session.PrepareInference(params)
	require.NoError(t, err)

	payload := &host.InferencePayload{
		Prompt:      params.Prompt,
		Model:       params.Model,
		InputLength: params.InputLength,
		MaxTokens:   params.MaxTokens,
		StartedAt:   params.StartedAt,
	}

	execIdx := int(prepared.diff.Nonce % uint64(len(session.clients)))
	execHost := session.clients[execIdx].(*InProcessClient).Host
	receipt, _, err := execHost.ChallengeReceipt(ctx, prepared.diff.Nonce, payload, []types.Diff{prepared.diff})
	require.NoError(t, err)
	require.NotEmpty(t, receipt)

	var confirmTx *types.DevshardTx
	for _, tx := range execHost.MempoolTxs() {
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == prepared.diff.Nonce {
			confirmTx = tx
			break
		}
	}
	require.NotNil(t, confirmTx)

	session.mu.Lock()
	session.addPendingTx(confirmTx)
	session.mu.Unlock()
	require.NoError(t, session.SendPendingDiff(ctx), "test setup must publish ConfirmStart before execution timeout")

	session.mu.Lock()
	session.nonceStates[prepared.diff.Nonce].confirmedAt = 1
	session.mu.Unlock()

	beforeDiffs := len(session.Diffs())
	for i, c := range session.clients {
		session.clients[i] = &timeoutRecoveryClient{HostClient: c, mempool: []*types.DevshardTx{confirmTx}}
	}

	_, err = session.HandleTimeout(ctx, prepared.diff.Nonce, time.Unix(0, 0), payload)
	require.Error(t, err)
	require.Contains(t, err.Error(), "insufficient votes")
	require.Len(t, session.Diffs(), beforeDiffs, "execution timeout must not publish ConfirmStart recovery diff")

	rec, ok := session.StateMachine().SnapshotState().Inferences[prepared.diff.Nonce]
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, rec.Status)
}

func TestHandleTimeout_ExecutionTimeoutPrefersPendingFinishOverTimeoutVotes(t *testing.T) {
	prevTimeoutBuffer := TimeoutBuffer
	TimeoutBuffer = 0
	t.Cleanup(func() {
		TimeoutBuffer = prevTimeoutBuffer
	})

	session, signers, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	prepared, err := session.PrepareInference(params)
	require.NoError(t, err)

	payload := &host.InferencePayload{
		Prompt:      params.Prompt,
		Model:       params.Model,
		InputLength: params.InputLength,
		MaxTokens:   params.MaxTokens,
		StartedAt:   params.StartedAt,
	}

	execIdx := int(prepared.diff.Nonce % uint64(len(session.clients)))
	execHost := session.clients[execIdx].(*InProcessClient).Host
	receipt, _, err := execHost.ChallengeReceipt(ctx, prepared.diff.Nonce, payload, []types.Diff{prepared.diff})
	require.NoError(t, err)
	require.NotEmpty(t, receipt)
	confirmTx := findRecoveryConfirmStart(execHost.MempoolTxs(), prepared.diff.Nonce)
	require.NotNil(t, confirmTx)

	require.NoError(t, session.ProcessResponse(execIdx, &host.HostResponse{
		Receipt:     receipt,
		ConfirmedAt: confirmTx.GetConfirmStart().ConfirmedAt,
	}, prepared.diff.Nonce))
	require.NoError(t, session.SendPendingDiff(ctx))
	require.Equal(t, types.StatusStarted, session.StateMachine().SnapshotState().Inferences[prepared.diff.Nonce].Status)

	require.Eventually(t, func() bool {
		return findRecoveryFinish(execHost.MempoolTxs(), prepared.diff.Nonce) != nil
	}, 5*time.Second, 20*time.Millisecond)
	finishTx := findRecoveryFinish(execHost.MempoolTxs(), prepared.diff.Nonce)
	require.NotNil(t, finishTx)

	session.mu.Lock()
	session.addPendingTx(finishTx)
	session.mu.Unlock()
	require.NotNil(t, findRecoveryFinish(session.PendingTxs(), prepared.diff.Nonce))
	session.mu.Lock()
	session.nonceStates[prepared.diff.Nonce].confirmedAt = 1
	session.mu.Unlock()

	for i, c := range session.clients {
		session.clients[i] = &timeoutVoteClient{
			HostClient: c,
			mockTimeoutVerifier: &mockTimeoutVerifier{
				accept:  true,
				signer:  signers[i],
				group:   session.group,
				slotIdx: i,
			},
		}
	}

	result, err := session.HandleTimeout(ctx, prepared.diff.Nonce, time.Unix(0, 0), nil)
	require.NoError(t, err, "pending FinishInference should be published instead of timeout votes")
	require.Equal(t, "execution", result.Reason)
	require.Equal(t, types.StatusFinished, session.StateMachine().SnapshotState().Inferences[prepared.diff.Nonce].Status)
}

func TestHandleTimeout_RefusedReject_UnrelatedMempool(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	prepared, err := session.PrepareInference(params)
	require.NoError(t, err)

	payload := &host.InferencePayload{
		Prompt:      params.Prompt,
		Model:       params.Model,
		InputLength: params.InputLength,
		MaxTokens:   params.MaxTokens,
		StartedAt:   params.StartedAt,
	}

	unrelated := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 999,
		ExecutorSig: []byte("other"),
		ConfirmedAt: 1,
	}}}
	for i, c := range session.clients {
		session.clients[i] = &timeoutRecoveryClient{HostClient: c, mempool: []*types.DevshardTx{unrelated}}
	}

	_, err = session.HandleTimeout(ctx, prepared.diff.Nonce, time.Unix(0, 0), payload)
	require.Error(t, err)
	require.Contains(t, err.Error(), "insufficient votes")

	rec, ok := session.StateMachine().SnapshotState().Inferences[prepared.diff.Nonce]
	require.True(t, ok)
	require.Equal(t, types.StatusPending, rec.Status, "unrelated recovery txs must not be treated as success")
}

func countRecoveryConfirmStart(txs []*types.DevshardTx, inferenceID uint64) int {
	var count int
	for _, tx := range txs {
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == inferenceID {
			count++
		}
	}
	return count
}

func countRecoveryFinish(txs []*types.DevshardTx, inferenceID uint64) int {
	var count int
	for _, tx := range txs {
		if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == inferenceID {
			count++
		}
	}
	return count
}

func findRecoveryConfirmStart(txs []*types.DevshardTx, inferenceID uint64) *types.DevshardTx {
	for _, tx := range txs {
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == inferenceID {
			return tx
		}
	}
	return nil
}

func findRecoveryFinish(txs []*types.DevshardTx, inferenceID uint64) *types.DevshardTx {
	for _, tx := range txs {
		if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == inferenceID {
			return tx
		}
	}
	return nil
}

func findPendingConfirm(txs []*types.DevshardTx, inferenceID uint64) *types.DevshardTx {
	for _, tx := range txs {
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == inferenceID {
			return tx
		}
	}
	return nil
}

func findPendingTimeout(txs []*types.DevshardTx, inferenceID uint64) *types.DevshardTx {
	for _, tx := range txs {
		if to := tx.GetTimeoutInference(); to != nil && to.InferenceId == inferenceID {
			return tx
		}
	}
	return nil
}

func findPendingErrorMiss(txs []*types.DevshardTx, inferenceID uint64) *types.DevshardTx {
	for _, tx := range txs {
		if em := tx.GetErrorMiss(); em != nil && em.InferenceId == inferenceID {
			return tx
		}
	}
	return nil
}
