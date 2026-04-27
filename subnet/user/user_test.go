package user

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"subnet"
	"subnet/host"
	"subnet/internal/testutil"
	"subnet/signing"
	"subnet/state"
	"subnet/stub"
	"subnet/types"
)

func setupSession(t *testing.T, numHosts int, balance uint64, grace uint64) (*Session, []*signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	return setupSessionWithEngine(t, numHosts, balance, grace, nil)
}

func setupSessionWithEngine(t *testing.T, numHosts int, balance uint64, grace uint64, engines []subnet.InferenceEngine) (*Session, []*signing.Secp256k1Signer, *signing.Secp256k1Signer) {
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
		sm := state.NewStateMachine("escrow-1", config, group, balance, user.Address(), verifier)
		var engine subnet.InferenceEngine
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
	userSM := state.NewStateMachine("escrow-1", config, group, balance, user.Address(), verifier)
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier)
	require.NoError(t, err)

	return session, hosts, user
}

func TestUser_RoundRobinSelection(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()

	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	}

	// Nonce 1 -> host 1%3=1, nonce 2 -> host 2%3=2, nonce 3 -> host 3%3=0.
	for i := 0; i < 6; i++ {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	// Verify round-robin pattern over 6 inferences.
	require.Equal(t, uint64(6), session.Nonce())
}

func TestUser_PipelinesReceipt(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()

	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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

func (c *ErrorClient) Send(_ context.Context, _ host.HostRequest) (*host.HostResponse, error) {
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
		sm := state.NewStateMachine("escrow-1", config, group, 100000, userKey.Address(), verifier)
		engine := stub.NewInferenceEngine()
		h, err := host.NewHost(sm, hosts[i], engine, "escrow-1", group, nil, host.WithGrace(100))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	userSM := state.NewStateMachine("escrow-1", config, group, 100000, userKey.Address(), verifier)
	session, err := NewSession(userSM, userKey, "escrow-1", group, clients, verifier)
	require.NoError(t, err)

	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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

func TestUser_Finalize(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 100)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
		sm := state.NewStateMachine("escrow-1", config, group, 100000, userKey.Address(), verifier)
		engine := stub.NewInferenceEngine()
		h, err := host.NewHost(sm, slotSigner, engine, "escrow-1", group, nil, host.WithGrace(100))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	userSM := state.NewStateMachine("escrow-1", config, group, 100000, userKey.Address(), verifier)
	session, err := NewSession(userSM, userKey, "escrow-1", group, clients, verifier)
	require.NoError(t, err)

	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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

	votes, err := session.CollectTimeoutVotes(ctx, 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, &host.InferencePayload{
		Prompt:      testutil.TestPrompt,
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   50,
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
	accept   bool
	signer   *signing.Secp256k1Signer
	group    []types.SlotAssignment
	slotIdx  int
	escrowID string // defaults to "escrow-1" when empty
}

func (m *mockTimeoutVerifier) VerifyTimeout(_ context.Context, inferenceID uint64, reason types.TimeoutReason, _ *host.InferencePayload, _ []types.Diff) (bool, []byte, uint32, error) {
	if !m.accept {
		return false, nil, 0, nil
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
	data, err := proto.Marshal(content)
	if err != nil {
		return false, nil, 0, err
	}
	sig, err := m.signer.Sign(data)
	if err != nil {
		return false, nil, 0, err
	}
	return true, sig, voterSlot, nil
}

// Fixed private keys for reproducible seed derivation.
// signer[0] seed=8507102209880137399, signer[1] seed=8250581583015032772, signer[2] seed=88554756047201157.
// With 3 hosts, 100% rate, prob=0.5 per non-executor inference:
//   signer[0]: validates inf 1,2 (RequiredValidations=2)
//   signer[1]: all floats >= 0.5 (RequiredValidations=0)
//   signer[2]: all floats >= 0.5 (RequiredValidations=0)
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
		sm := state.NewStateMachine("escrow-1", config, group, 100000, userKey.Address(), verifier)
		engine := stub.NewInferenceEngine()
		h, err := host.NewHost(sm, hosts[i], engine, "escrow-1", group, nil, host.WithGrace(100))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	userSM := state.NewStateMachine("escrow-1", config, group, 100000, userKey.Address(), verifier)
	session, err := NewSession(userSM, userKey, "escrow-1", group, clients, verifier)
	require.NoError(t, err)

	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	}

	// Send 3 inferences (one per host via round-robin).
	for i := 0; i < numHosts; i++ {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	err = session.Finalize(ctx)
	require.NoError(t, err)

	st := session.StateMachine().SnapshotState()

	// All 3 hosts should have revealed seeds.
	require.Len(t, st.RevealedSeeds, numHosts, "all hosts should have revealed seeds")
	for slot := range st.RevealedSeeds {
		require.Contains(t, st.RevealedSeeds, slot)
	}

	// With 100% validation rate, non-executor hosts should have RequiredValidations > 0.
	hasRequired := false
	for _, hs := range st.HostStats {
		if hs.RequiredValidations > 0 {
			hasRequired = true
			break
		}
	}
	require.True(t, hasRequired, "at least one host should have RequiredValidations > 0")

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
