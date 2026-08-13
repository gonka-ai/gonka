package user

import (
	"context"
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

func TestPrepareInference_StartInferenceIsMandatory(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100, 10)

	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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

func (c *ErrorClient) Send(_ context.Context, _ host.HostRequest, _ io.Writer, _ func()) (*host.HostResponse, error) {
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

func TestSession_SendPendingDiffRetriesReachableHost(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
	// claimSlot, when non-nil, overrides the returned voter_slot (used to
	// simulate a byzantine verifier claiming another validator's slot).
	claimSlot *uint32
	// signSlot, when non-nil, is the voter_slot embedded in the signed
	// TimeoutVoteContent. Defaults to the returned voter_slot. Set differently
	// from claimSlot/own slot to simulate a signature/slot mismatch.
	signSlot *uint32
	// delay, when >0, sleeps before returning (honest slow responders).
	delay time.Duration
}

func (m *mockTimeoutVerifier) VerifyTimeout(ctx context.Context, inferenceID uint64, reason types.TimeoutReason, _ *host.InferencePayload, _ []types.Diff) (bool, []byte, uint32, error) {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return false, nil, 0, ctx.Err()
		}
	}
	if !m.accept {
		return false, nil, 0, nil
	}
	eid := m.escrowID
	if eid == "" {
		eid = "escrow-1"
	}
	voterSlot := m.group[m.slotIdx].SlotID
	if m.claimSlot != nil {
		voterSlot = *m.claimSlot
	}
	signedSlot := voterSlot
	if m.signSlot != nil {
		signedSlot = *m.signSlot
	}
	content := &types.TimeoutVoteContent{
		EscrowId:    eid,
		InferenceId: inferenceID,
		Reason:      reason,
		Accept:      true,
		VoterSlot:   signedSlot,
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

// TestCollectTimeoutVotes_RejectsSpoofedVoterSlot ensures a byzantine verifier
// cannot terminate collection early by returning accept=true with a high-weight
// validator's voter_slot while signing with its own key. Weight and early-exit
// must bind to the contacted responder; the spoofed vote must be dropped so
// honest delayed responses can still form a valid quorum.
func TestCollectTimeoutVotes_RejectsSpoofedVoterSlot(t *testing.T) {
	// 4 validators, slots [1, 1, 5, 1] (total 8). VoteThreshold = 4; need >4.
	// Signer[2] owns weight 5. Malicious signer[0] (weight 1) claims one of
	// signer[2]'s slots. Without the bind check, a single spoofed response
	// would push running weight to 5 and cancel the remaining honest RPCs.
	signers := make([]*signing.Secp256k1Signer, 4)
	for i := range signers {
		signers[i] = testutil.MustGenerateKey(t)
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeMultiSlotGroup(signers, []int{1, 1, 5, 1})
	numSlots := len(group)
	config := types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		VoteThreshold:    uint32(numSlots) / 2, // 8/2 = 4
	}
	verifier := signing.NewSecp256k1Verifier()

	clients := make([]HostClient, numSlots)
	for i, slot := range group {
		slotSigner := signerForSlot(t, signers, slot)
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
	_, err = session.SendInference(ctx, InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})
	require.NoError(t, err)

	// Executor = group[1 % 8] (nonce 1). First index owned by signer[1].
	executorIdx := int(1 % uint64(numSlots))
	executorAddr := group[executorIdx].ValidatorAddress

	// Pick a slot owned by high-weight signer[2] for the spoof claim.
	var heavySlot uint32
	for _, slot := range group {
		if slot.ValidatorAddress == signers[2].Address() {
			heavySlot = slot.SlotID
			break
		}
	}

	verifiers := make(map[int]TimeoutVerifier)
	for i, slot := range group {
		if slot.ValidatorAddress == executorAddr {
			continue
		}
		slotSigner := signerForSlot(t, signers, slot)
		m := &mockTimeoutVerifier{
			accept:  true,
			signer:  slotSigner,
			group:   group,
			slotIdx: i,
			delay:   50 * time.Millisecond, // honest responders are slower
		}
		// First index owned by signer[0]: return immediately with spoofed slot.
		if slot.ValidatorAddress == signers[0].Address() {
			claim := heavySlot
			m.claimSlot = &claim
			m.delay = 0
		}
		verifiers[i] = m
	}

	votes, err := session.CollectTimeoutVotes(ctx, 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, &host.InferencePayload{
		Prompt:      testutil.TestPrompt,
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   50,
		StartedAt:   1000,
	}, verifiers, nil)
	require.NoError(t, err)

	// Precondition: the spoofed claim alone carries enough weight to have
	// terminated collection early, so this test only passes because the
	// responder bind rejected it.
	require.Greater(t, session.sm.AddressSlotCount(signers[2].Address()), config.VoteThreshold,
		"spoofed slot must be heavy enough to trip early exit on its own")

	// The spoofed vote and the honest signer[2] vote both claim slots owned by
	// signer[2]; a collected spoof would show up as a duplicate voter address,
	// which the state machine rejects for the whole MsgTimeoutInference.
	seenAddrs := make(map[string]bool, len(votes))
	for _, v := range votes {
		addr := session.sm.SlotAddress(v.VoterSlot)
		require.False(t, seenAddrs[addr], "duplicate voter address %s implies a spoofed vote was collected", addr)
		seenAddrs[addr] = true
	}
	require.NotEmpty(t, votes, "honest delayed verifiers must still be collected")
	require.True(t, session.HasSufficientTimeoutVotes(votes),
		"honest delayed votes must still form a quorum after spoofed response is dropped")

	// Every collected vote must satisfy the same rule applyTimeout enforces:
	// the recovered signer owns the claimed slot. This is the property the
	// vulnerability broke — collection returned votes the state machine would
	// later reject, after the honest RPCs had already been cancelled.
	requireVotesPassStateMachineRules(t, verifier, "escrow-1", 1,
		types.TimeoutReason_TIMEOUT_REASON_REFUSED, votes, session)
}

// requireVotesPassStateMachineRules mirrors StateMachine.applyTimeout's per-vote
// signature check so collection tests can assert that everything they return
// would be accepted on apply.
func requireVotesPassStateMachineRules(
	t *testing.T,
	verifier signing.Verifier,
	escrowID string,
	inferenceID uint64,
	reason types.TimeoutReason,
	votes []*types.TimeoutVote,
	session *Session,
) {
	t.Helper()
	for _, v := range votes {
		voterAddr := session.sm.SlotAddress(v.VoterSlot)
		require.NotEmpty(t, voterAddr, "vote claims slot %d which is not in the group", v.VoterSlot)

		data, err := proto.MarshalOptions{Deterministic: true}.Marshal(&types.TimeoutVoteContent{
			EscrowId:    escrowID,
			InferenceId: inferenceID,
			Reason:      reason,
			Accept:      v.Accept,
			VoterSlot:   v.VoterSlot,
		})
		require.NoError(t, err)

		recovered, err := verifier.RecoverAddress(data, v.Signature)
		require.NoError(t, err, "vote from slot %d has an unrecoverable signature", v.VoterSlot)
		require.Equal(t, voterAddr, recovered,
			"vote from slot %d recovers to %s, not the slot owner", v.VoterSlot, recovered)
	}
}

// TestCollectTimeoutVotes_RejectsSignedSlotMismatch covers the case where a
// verifier returns its own voter_slot (so the responder-bind check passes) but
// the signature was produced over TimeoutVoteContent with a different
// voter_slot. Collection must drop that vote.
func TestCollectTimeoutVotes_RejectsSignedSlotMismatch(t *testing.T) {
	signers := make([]*signing.Secp256k1Signer, 3)
	for i := range signers {
		signers[i] = testutil.MustGenerateKey(t)
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(signers)
	config := testutil.DefaultConfig(len(group))
	verifier := signing.NewSecp256k1Verifier()

	clients := make([]HostClient, len(group))
	for i := range signers {
		sm := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, userKey.Address(), verifier)
		h, err := host.NewHost(sm, signers[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(100))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, userKey.Address(), verifier)
	session, err := NewSession(userSM, userKey, "escrow-1", group, clients, verifier)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = session.SendInference(ctx, InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})
	require.NoError(t, err)

	executorIdx := int(1 % uint64(len(group)))
	foreignSlot := group[(executorIdx+1)%len(group)].SlotID
	// Pick a non-executor host that is not the foreign slot owner if possible.
	badIdx := 0
	if badIdx == executorIdx {
		badIdx = 2
	}

	verifiers := make(map[int]TimeoutVerifier)
	for i := range group {
		if i == executorIdx {
			continue
		}
		m := &mockTimeoutVerifier{
			accept:  true,
			signer:  signers[i],
			group:   group,
			slotIdx: i,
		}
		if i == badIdx {
			sign := foreignSlot
			if sign == group[i].SlotID {
				sign = group[(i+1)%len(group)].SlotID
			}
			m.signSlot = &sign // return own slot, sign a different one
		}
		verifiers[i] = m
	}

	votes, err := session.CollectTimeoutVotes(ctx, 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, &host.InferencePayload{
		Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	}, verifiers, nil)
	require.NoError(t, err)

	for _, v := range votes {
		require.NotEqual(t, group[badIdx].SlotID, v.VoterSlot,
			"vote with signature/slot mismatch must be dropped")
	}
	requireVotesPassStateMachineRules(t, verifier, "escrow-1", 1,
		types.TimeoutReason_TIMEOUT_REASON_REFUSED, votes, session)
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

func (m *concurrencyMockVerifier) VerifyTimeout(ctx context.Context, inferenceID uint64, reason types.TimeoutReason, _ *host.InferencePayload, _ []types.Diff) (bool, []byte, uint32, error) {
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
			return false, nil, 0, ctx.Err()
		}
	}
	voterSlot := m.group[m.slotIdx].SlotID
	content := &types.TimeoutVoteContent{
		EscrowId:    "escrow-1",
		InferenceId: inferenceID,
		Reason:      reason,
		Accept:      true,
		VoterSlot:   voterSlot,
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	}

	type collectResult struct {
		votes []*types.TimeoutVote
		err   error
	}
	resultsCh := make(chan collectResult, 2)

	for i := 0; i < 2; i++ {
		go func() {
			votes, err := session.CollectTimeoutVotes(ctx, nonce, types.TimeoutReason_TIMEOUT_REASON_REFUSED, payload, buildVerifiers(), nil)
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	}

	done := make(chan error, 1)
	go func() {
		_, err := session.CollectTimeoutVotes(ctx, nonce, types.TimeoutReason_TIMEOUT_REASON_REFUSED, payload, verifiers, nil)
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	}

	// Launch the blocking first call so all verifier slots are occupied.
	firstDone := make(chan struct{})
	go func() {
		_, _ = session.CollectTimeoutVotes(ctx, nonce, types.TimeoutReason_TIMEOUT_REASON_REFUSED, payload, firstVerifiers, nil)
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
	votes, err := session.CollectTimeoutVotes(ctx, nonce, types.TimeoutReason_TIMEOUT_REASON_REFUSED, payload, secondVerifiers, nil)
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := session.CollectTimeoutVotes(ctx, nonce, types.TimeoutReason_TIMEOUT_REASON_REFUSED, payload, buildVerifiers(), nil)
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
	session.finalizeClients = nil
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
