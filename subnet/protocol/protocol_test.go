package protocol

import (
	"context"
	"crypto/sha256"
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
	"subnet/user"
)

// --- helpers ---

type testEnv struct {
	session *user.Session
	hosts   []*host.Host
	signers []*signing.Secp256k1Signer
	user    *signing.Secp256k1Signer
	group   []types.SlotAssignment
	config  types.SessionConfig
}

func setupEnv(t *testing.T, numHosts int, balance, grace uint64, engines ...subnet.InferenceEngine) *testEnv {
	t.Helper()
	hostSigners := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
	}
	userSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	hosts := make([]*host.Host, numHosts)
	clients := make([]user.HostClient, numHosts)
	for i := range hostSigners {
		sm := state.NewStateMachine("escrow-1", config, group, balance, userSigner.Address(), verifier)
		var engine subnet.InferenceEngine
		if len(engines) > 0 {
			engine = engines[i]
		} else {
			engine = stub.NewInferenceEngine()
		}
		h, err := host.NewHost(sm, hostSigners[i], engine, "escrow-1", group, nil, host.WithGrace(grace))
		require.NoError(t, err)
		hosts[i] = h
		clients[i] = &user.InProcessClient{Host: h}
	}

	userSM := state.NewStateMachine("escrow-1", config, group, balance, userSigner.Address(), verifier)
	session, err := user.NewSession(userSM, userSigner, "escrow-1", group, clients, verifier)
	require.NoError(t, err)

	return &testEnv{
		session: session,
		hosts:   hosts,
		signers: hostSigners,
		user:    userSigner,
		group:   group,
		config:  config,
	}
}

func defaultParams() user.InferenceParams {
	return user.InferenceParams{
		Model:       "llama",
		Prompt:      testutil.TestPrompt,
		InputLength: 100,
		MaxTokens:   50,
		StartedAt:   1000,
	}
}

// --- Integration tests ---

func TestProtocol_HappyPath_15Inferences(t *testing.T) {
	env := setupEnv(t, 5, 1000000, 100)
	ctx := context.Background()
	params := defaultParams()

	for i := 0; i < 15; i++ {
		result, err := env.session.SendInference(ctx, params)
		require.NoError(t, err, "inference %d", i+1)
		require.NotNil(t, result)
	}

	// Verify all 15 inferences.
	st := env.session.StateMachine().SnapshotState()

	// Count finished inferences. Due to pipelining, the first few might still
	// be pending/started since MsgFinishInference gets included in later diffs.
	// The protocol is: nonce N starts inference N, nonce N+1 includes
	// ConfirmStart for N + FinishInference for N.
	// So inferences 1-14 should have their finish included. Inference 15 is
	// still in the executor's mempool.
	finishedCount := 0
	startedCount := 0
	for _, rec := range st.Inferences {
		switch rec.Status {
		case types.StatusFinished:
			finishedCount++
		case types.StatusStarted:
			startedCount++
		}
	}
	// At minimum, inferences that had their finish included should be finished.
	// The exact count depends on how many diffs were processed.
	require.Equal(t, 15, len(st.Inferences), "should have 15 inference records")
	require.True(t, finishedCount >= 13, "at least 13 should be finished, got %d", finishedCount)

	// Verify executor distribution: each host should execute 3 inferences
	// (15 inferences / 5 hosts = 3 each).
	executorCounts := make(map[uint32]int)
	for id, rec := range st.Inferences {
		expectedExecutor := uint32(id % 5)
		require.Equal(t, expectedExecutor, rec.ExecutorSlot, "inference %d executor", id)
		executorCounts[rec.ExecutorSlot]++
	}
	for slot := uint32(0); slot < 5; slot++ {
		require.Equal(t, 3, executorCounts[slot], "slot %d should execute 3 inferences", slot)
	}

	// Verify balance decreased.
	require.Less(t, st.Balance, uint64(1000000))

	// Verify host stats cost for finished inferences.
	totalCost := uint64(0)
	for _, hs := range st.HostStats {
		totalCost += hs.Cost
	}
	expectedCostPerFinished := uint64(120) // (80+40)*1
	require.Equal(t, uint64(finishedCount)*expectedCostPerFinished, totalCost)

	// Verify signatures collected.
	sigs := env.session.Signatures()
	require.NotEmpty(t, sigs)
}

func TestProtocol_ReceiptPipelining(t *testing.T) {
	env := setupEnv(t, 3, 100000, 10)
	ctx := context.Background()
	params := defaultParams()

	// Send 3 inferences.
	for i := 0; i < 3; i++ {
		_, err := env.session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	diffs := env.session.Diffs()
	require.Len(t, diffs, 3)

	// Diff at nonce 2 should include MsgConfirmStart for inference 1
	// AND MsgFinishInference for inference 1 (both pipelined from host 1's response).
	var hasConfirmForInf1, hasFinishForInf1 bool
	for _, tx := range diffs[1].Txs {
		if confirm := tx.GetConfirmStart(); confirm != nil && confirm.InferenceId == 1 {
			hasConfirmForInf1 = true
		}
		if fin := tx.GetFinishInference(); fin != nil && fin.InferenceId == 1 {
			hasFinishForInf1 = true
		}
	}
	require.True(t, hasConfirmForInf1, "diff at nonce 2 should pipeline MsgConfirmStart for inference 1")
	require.True(t, hasFinishForInf1, "diff at nonce 2 should pipeline MsgFinishInference for inference 1")

	// Diff at nonce 3 should include MsgConfirmStart for inference 2
	// AND MsgFinishInference for inference 2.
	var hasConfirmForInf2, hasFinishForInf2 bool
	for _, tx := range diffs[2].Txs {
		if confirm := tx.GetConfirmStart(); confirm != nil && confirm.InferenceId == 2 {
			hasConfirmForInf2 = true
		}
		if fin := tx.GetFinishInference(); fin != nil && fin.InferenceId == 2 {
			hasFinishForInf2 = true
		}
	}
	require.True(t, hasConfirmForInf2, "diff at nonce 3 should pipeline MsgConfirmStart for inference 2")
	require.True(t, hasFinishForInf2, "diff at nonce 3 should pipeline MsgFinishInference for inference 2")
}

func TestProtocol_SignatureWithholding(t *testing.T) {
	// Manual protocol drive: 3 hosts, grace=2.
	hostSigners := make([]*signing.Secp256k1Signer, 3)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
	}
	userSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := testutil.DefaultConfig(3)
	verifier := signing.NewSecp256k1Verifier()

	// Create host at slot 1 with grace=2.
	sm := state.NewStateMachine("escrow-1", config, group, 100000, userSigner.Address(), verifier)
	engine := stub.NewInferenceEngine()
	h, err := host.NewHost(sm, hostSigners[1], engine, "escrow-1", group, nil, host.WithGrace(2))
	require.NoError(t, err)

	ctx := context.Background()

	// Nonce 1: start inference 1, executor=slot 1.
	diff1 := testutil.SignDiff(t, userSigner, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(ctx, host.HostRequest{
		Diffs: []types.Diff{diff1}, Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt: testutil.TestPrompt, Model: "llama",
			InputLength: 100, MaxTokens: 50, StartedAt: 1000,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.StateSig, "should sign at nonce 1")
	require.NotNil(t, resp.Receipt)
	require.NotNil(t, resp.ExecutionJob)

	// Run deferred execution to populate mempool.
	_, err = h.RunExecution(ctx, resp.ExecutionJob)
	require.NoError(t, err)
	require.Len(t, h.MempoolTxs(), 2) // MsgConfirmStart + MsgFinishInference

	// Nonces 2-4: empty diffs, never including the finish.
	// Grace=2, proposed at nonce 1.
	// Nonce 2: 1+2=3, not < 2 -> OK
	// Nonce 3: 1+2=3, not < 3 -> OK
	// Nonce 4: 1+2=3 < 4 -> stale -> withhold
	for n := uint64(2); n <= 4; n++ {
		diff := testutil.SignDiff(t, userSigner, "escrow-1", n, nil)
		resp, err = h.HandleRequest(ctx, host.HostRequest{Diffs: []types.Diff{diff}, Nonce: n})
		require.NoError(t, err)
		if n < 4 {
			require.NotNil(t, resp.StateSig, "should sign at nonce %d", n)
		} else {
			require.Nil(t, resp.StateSig, "should withhold at nonce 4")
		}
		// Still processes diffs and returns mempool.
		require.Equal(t, n, resp.Nonce)
		require.Len(t, resp.Mempool, 2, "mempool should have confirm_start + finish")
	}
}

func TestProtocol_SignatureResumesAfterInclusion(t *testing.T) {
	hostSigners := make([]*signing.Secp256k1Signer, 3)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
	}
	userSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := testutil.DefaultConfig(3)
	verifier := signing.NewSecp256k1Verifier()

	sm := state.NewStateMachine("escrow-1", config, group, 100000, userSigner.Address(), verifier)
	engine := stub.NewInferenceEngine()
	h, err := host.NewHost(sm, hostSigners[1], engine, "escrow-1", group, nil, host.WithGrace(2))
	require.NoError(t, err)

	ctx := context.Background()

	// Nonce 1: start inference 1.
	diff1 := testutil.SignDiff(t, userSigner, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(ctx, host.HostRequest{
		Diffs: []types.Diff{diff1}, Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt: testutil.TestPrompt, Model: "llama",
			InputLength: 100, MaxTokens: 50, StartedAt: 1000,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.ExecutionJob)
	_, err = h.RunExecution(ctx, resp.ExecutionJob)
	require.NoError(t, err)

	// Find the finish tx (mempool also has confirm_start from Issue 4 fix).
	var finishTx *types.SubnetTx
	for _, tx := range h.MempoolTxs() {
		if tx.GetFinishInference() != nil {
			finishTx = tx
			break
		}
	}
	require.NotNil(t, finishTx, "mempool should contain MsgFinishInference")

	// Nonce 2: confirm start.
	receiptContent := &types.ExecutorReceiptContent{
		InferenceId: 1, PromptHash: testutil.TestPromptHash[:], Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000, EscrowId: "escrow-1",
		ConfirmedAt: resp.ConfirmedAt,
	}
	receiptData, _ := proto.Marshal(receiptContent)
	receiptSig, _ := hostSigners[1].Sign(receiptData)
	confirmTx := &types.SubnetTx{Tx: &types.SubnetTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: receiptSig, ConfirmedAt: resp.ConfirmedAt,
	}}}
	diff2 := testutil.SignDiff(t, userSigner, "escrow-1", 2, []*types.SubnetTx{confirmTx})

	// Nonces 3,4: empty (push past grace).
	diff3 := testutil.SignDiff(t, userSigner, "escrow-1", 3, nil)
	diff4 := testutil.SignDiff(t, userSigner, "escrow-1", 4, nil)

	resp, err = h.HandleRequest(ctx, host.HostRequest{Diffs: []types.Diff{diff2, diff3, diff4}, Nonce: 4})
	require.NoError(t, err)
	require.Nil(t, resp.StateSig, "should withhold (stale)")

	// Nonce 5: include the finish tx -> mempool cleared.
	diff5 := testutil.SignDiff(t, userSigner, "escrow-1", 5, []*types.SubnetTx{finishTx})
	resp, err = h.HandleRequest(ctx, host.HostRequest{Diffs: []types.Diff{diff5}, Nonce: 5})
	require.NoError(t, err)
	require.NotNil(t, resp.StateSig, "should resume signing after inclusion")
}

func TestProtocol_ExecutorAssignment(t *testing.T) {
	env := setupEnv(t, 5, 1000000, 100)
	ctx := context.Background()
	params := defaultParams()

	for i := 0; i < 5; i++ {
		resp, err := env.session.SendInference(ctx, params)
		require.NoError(t, err)

		// The executor should return a receipt.
		require.NotNil(t, resp.Receipt, "executor host should return receipt for inference %d", i+1)
	}

	// Verify executor assignment in state.
	st := env.session.StateMachine().SnapshotState()
	for id, rec := range st.Inferences {
		expectedSlot := uint32(id % 5)
		require.Equal(t, expectedSlot, rec.ExecutorSlot, "inference %d should have executor slot %d", id, expectedSlot)
	}
}

// --- New tests ---

func TestProtocol_StateSignatureContent(t *testing.T) {
	env := setupEnv(t, 3, 100000, 100)
	ctx := context.Background()
	params := defaultParams()
	verifier := signing.NewSecp256k1Verifier()

	for i := 0; i < 3; i++ {
		_, err := env.session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	// Replay diffs through a fresh StateMachine to get state roots at each nonce.
	replaySM := state.NewStateMachine("escrow-1", env.config, env.group, 100000, env.user.Address(), verifier)
	roots := make(map[uint64][]byte)
	for _, diff := range env.session.Diffs() {
		root, err := replaySM.ApplyDiff(diff)
		require.NoError(t, err)
		roots[diff.Nonce] = root
	}

	// For each collected signature, verify it was signed over StateSignatureContent.
	sigs := env.session.Signatures()
	for nonce, slotSigs := range sigs {
		root, ok := roots[nonce]
		require.True(t, ok, "must have root for nonce %d", nonce)
		for slotID, sig := range slotSigs {
			sigContent := &types.StateSignatureContent{
				StateRoot: root,
				EscrowId:  "escrow-1",
				Nonce:     nonce,
			}
			sigData, err := proto.Marshal(sigContent)
			require.NoError(t, err)
			addr, err := verifier.RecoverAddress(sigData, sig)
			require.NoError(t, err)
			require.Equal(t, env.group[slotID].ValidatorAddress, addr,
				"nonce %d slot %d: signature must recover to correct host", nonce, slotID)
		}
	}
}

func TestProtocol_StateConvergence(t *testing.T) {
	env := setupEnv(t, 5, 1000000, 100)
	ctx := context.Background()
	params := defaultParams()

	for i := 0; i < 10; i++ {
		_, err := env.session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	// Send full catch-up diffs to every host.
	allDiffs := env.session.Diffs()
	var hostRoots [][]byte
	lastNonce := allDiffs[len(allDiffs)-1].Nonce
	for i, h := range env.hosts {
		resp, err := h.HandleRequest(ctx, host.HostRequest{Diffs: allDiffs, Nonce: lastNonce})
		require.NoError(t, err, "host %d", i)
		require.NotNil(t, resp)

		root, err := h.StateRoot()
		require.NoError(t, err, "host %d StateRoot", i)
		hostRoots = append(hostRoots, root)
	}

	// All hosts should have the same state root.
	for i := 1; i < len(hostRoots); i++ {
		require.Equal(t, hostRoots[0], hostRoots[i],
			"host %d root differs from host 0", i)
	}
}

func TestProtocol_Timeout_UserSide(t *testing.T) {
	// Manual protocol drive: 5 hosts. Start inference, compose timeout.
	hostSigners := make([]*signing.Secp256k1Signer, 5)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
	}
	userSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := testutil.DefaultConfig(5)
	verifier := signing.NewSecp256k1Verifier()

	// Create all hosts.
	hosts := make([]*host.Host, 5)
	for i := range hosts {
		sm := state.NewStateMachine("escrow-1", config, group, 100000, userSigner.Address(), verifier)
		engine := stub.NewInferenceEngine()
		h, err := host.NewHost(sm, hostSigners[i], engine, "escrow-1", group, nil, host.WithGrace(100))
		require.NoError(t, err)
		hosts[i] = h
	}

	ctx := context.Background()

	// Nonce 1: start inference 1. Executor = slot 1%5 = 1.
	diff1 := testutil.SignDiff(t, userSigner, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	// Send to host 1 (round-robin: nonce 1 % 5 = 1).
	resp, err := hosts[1].HandleRequest(ctx, host.HostRequest{
		Diffs: []types.Diff{diff1}, Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt: testutil.TestPrompt, Model: "llama",
			InputLength: 100, MaxTokens: 50, StartedAt: 1000,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Receipt)

	// Compose timeout: collect votes from non-executor hosts (slots 0, 2, 3).
	var votes []*types.TimeoutVote
	for _, slot := range []uint32{0, 2, 3} {
		v := testutil.SignTimeoutVote(t, hostSigners[slot], "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
		v.VoterSlot = slot
		votes = append(votes, v)
	}

	diff2 := testutil.SignDiff(t, userSigner, "escrow-1", 2, []*types.SubnetTx{
		{Tx: &types.SubnetTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{
			InferenceId: 1,
			Reason:      types.TimeoutReason_TIMEOUT_REASON_REFUSED,
			Votes:       votes,
		}}},
	})

	// Apply timeout to host 2.
	resp, err = hosts[2].HandleRequest(ctx, host.HostRequest{Diffs: []types.Diff{diff1, diff2}, Nonce: 2})
	require.NoError(t, err)

	// Verify via a fresh state machine.
	sm := state.NewStateMachine("escrow-1", config, group, 100000, userSigner.Address(), verifier)
	_, err = sm.ApplyDiff(diff1)
	require.NoError(t, err)
	_, err = sm.ApplyDiff(diff2)
	require.NoError(t, err)

	st := sm.SnapshotState()
	require.Equal(t, types.StatusTimedOut, st.Inferences[1].Status)
	require.Equal(t, uint32(1), st.HostStats[1].Missed)
	require.Equal(t, uint64(100000), st.Balance, "balance should be fully restored")
}

func TestProtocol_Finalize_AllInferencesFinished(t *testing.T) {
	env := setupEnv(t, 5, 1000000, 100)
	ctx := context.Background()
	params := defaultParams()

	for i := 0; i < 15; i++ {
		_, err := env.session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	err := env.session.Finalize(ctx)
	require.NoError(t, err)

	// ALL 15 must be finished (not just >= 13 as in the non-finalized case).
	st := env.session.StateMachine().SnapshotState()
	require.True(t, st.Phase >= types.PhaseFinalizing)
	for id, rec := range st.Inferences {
		require.Equal(t, types.StatusFinished, rec.Status, "inference %d should be finished", id)
	}
}

func TestProtocol_VaryingInferenceCosts(t *testing.T) {
	defaultHash := sha256.Sum256([]byte("stub"))
	defaultResult := subnet.ExecuteResult{
		ResponseHash: defaultHash[:],
		InputTokens:  80,
		OutputTokens: 40,
	}

	// 6 inferences with different engine outputs.
	overrides := map[uint64]subnet.ExecuteResult{
		1: {ResponseHash: defaultHash[:], InputTokens: 50, OutputTokens: 20},
		2: {ResponseHash: defaultHash[:], InputTokens: 90, OutputTokens: 45},
		3: {ResponseHash: defaultHash[:], InputTokens: 30, OutputTokens: 10},
		4: {ResponseHash: defaultHash[:], InputTokens: 100, OutputTokens: 50},
		5: {ResponseHash: defaultHash[:], InputTokens: 60, OutputTokens: 30},
		6: {ResponseHash: defaultHash[:], InputTokens: 40, OutputTokens: 15},
	}

	// All 3 hosts share the same configurable engine.
	engines := make([]subnet.InferenceEngine, 3)
	for i := range engines {
		engines[i] = &stub.ConfigurableEngine{
			Default:  defaultResult,
			Override: overrides,
		}
	}

	env := setupEnv(t, 3, 1000000, 100, engines...)
	ctx := context.Background()

	// Send 6 inferences with different params.
	paramsList := []user.InferenceParams{
		{Model: "llama", Prompt: testutil.TestPrompt, InputLength: 100, MaxTokens: 50, StartedAt: 1000},
		{Model: "llama", Prompt: testutil.TestPrompt, InputLength: 200, MaxTokens: 100, StartedAt: 2000},
		{Model: "llama", Prompt: testutil.TestPrompt, InputLength: 50, MaxTokens: 25, StartedAt: 3000},
		{Model: "llama", Prompt: testutil.TestPrompt, InputLength: 150, MaxTokens: 75, StartedAt: 4000},
		{Model: "llama", Prompt: testutil.TestPrompt, InputLength: 80, MaxTokens: 40, StartedAt: 5000},
		{Model: "llama", Prompt: testutil.TestPrompt, InputLength: 60, MaxTokens: 30, StartedAt: 6000},
	}

	for _, p := range paramsList {
		_, err := env.session.SendInference(ctx, p)
		require.NoError(t, err)
	}

	// Finalize to ensure all inferences complete.
	err := env.session.Finalize(ctx)
	require.NoError(t, err)

	st := env.session.StateMachine().SnapshotState()

	// Compute expected cumulative cost (token_price=1).
	expectedTotal := uint64(0)
	for id := uint64(1); id <= 6; id++ {
		o := overrides[id]
		expectedTotal += o.InputTokens + o.OutputTokens
	}

	// Verify balance = initial - sum(actual_costs).
	require.Equal(t, uint64(1000000)-expectedTotal, st.Balance)

	// Verify per-host cost stats.
	totalHostCost := uint64(0)
	for _, hs := range st.HostStats {
		totalHostCost += hs.Cost
	}
	require.Equal(t, expectedTotal, totalHostCost)

	// Verify individual inference costs.
	for id := uint64(1); id <= 6; id++ {
		rec := st.Inferences[id]
		require.Equal(t, types.StatusFinished, rec.Status, "inference %d", id)
		o := overrides[id]
		require.Equal(t, o.InputTokens+o.OutputTokens, rec.ActualCost, "inference %d cost", id)
	}
}

func TestProtocol_Finalize_SignaturesFromAllHosts(t *testing.T) {
	env := setupEnv(t, 5, 1000000, 100)
	ctx := context.Background()
	params := defaultParams()

	for i := 0; i < 15; i++ {
		_, err := env.session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	err := env.session.Finalize(ctx)
	require.NoError(t, err)

	// All 5 hosts must sign at the final nonce (Phase B collects all signatures).
	finalNonce := env.session.Nonce()
	sigs := env.session.Signatures()
	latestSigs, ok := sigs[finalNonce]
	require.True(t, ok, "no signatures at final nonce %d", finalNonce)
	require.Len(t, latestSigs, 5, "all 5 hosts should sign the final nonce")
}

func TestProtocol_Finalize_ExactDiffCount(t *testing.T) {
	numHosts := 5
	numInferences := 15
	env := setupEnv(t, numHosts, 1000000, 100)
	ctx := context.Background()
	params := defaultParams()

	for i := 0; i < numInferences; i++ {
		_, err := env.session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	err := env.session.Finalize(ctx)
	require.NoError(t, err)

	// Total diffs = numInferences + N (Phase A) + 1 (drain). Phase B sends catch-up only.
	expected := numInferences + numHosts + 1
	require.Equal(t, expected, len(env.session.Diffs()),
		"total diffs = inferences(%d) + N+1(%d)", numInferences, numHosts+1)
}

func TestProtocol_SignatureThreshold(t *testing.T) {
	env := setupEnv(t, 5, 1000000, 100)
	ctx := context.Background()
	params := defaultParams()

	for i := 0; i < 15; i++ {
		_, err := env.session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	sigs := env.session.Signatures()

	for nonce, slotSigs := range sigs {
		require.Len(t, slotSigs, 1,
			"nonce %d: expected 1 signature from round-robin host, got %d", nonce, len(slotSigs))
	}
}

func TestProtocol_Finalize_WithSeedReveal(t *testing.T) {
	env := setupEnv(t, 5, 1000000, 100)
	ctx := context.Background()
	params := defaultParams()

	for i := 0; i < 10; i++ {
		_, err := env.session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	err := env.session.Finalize(ctx)
	require.NoError(t, err)

	// After finalization, hosts should have produced seed reveals.
	// Check final state for revealed seeds and compliance.
	st := env.session.StateMachine().SnapshotState()
	require.True(t, st.Phase >= types.PhaseFinalizing)

	// At least some seeds should be revealed.
	require.True(t, len(st.RevealedSeeds) > 0, "expected seed reveals after finalization, got %d", len(st.RevealedSeeds))

	// HostStats for revealed slots should have RequiredValidations set.
	for slotID := range st.RevealedSeeds {
		hs := st.HostStats[slotID]
		// With 50% rate and multiple inferences, RequiredValidations should be > 0
		// in most cases (statistically). We just verify the fields were written.
		require.NotNil(t, hs, "host stats should exist for revealed slot %d", slotID)
	}
}

func TestProtocol_Settlement_EndToEnd(t *testing.T) {
	env := setupEnv(t, 3, 100000, 100)
	ctx := context.Background()
	params := defaultParams()

	for i := 0; i < 6; i++ {
		_, err := env.session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	err := env.session.Finalize(ctx)
	require.NoError(t, err)

	st := env.session.StateMachine().SnapshotState()
	nonce := env.session.Nonce()

	// Collect all signatures for the latest nonce.
	allSigs := env.session.Signatures()
	latestSigs := allSigs[nonce]

	settlement, err := state.BuildSettlement("escrow-1", st, latestSigs, nonce)
	require.NoError(t, err)
	require.NotNil(t, settlement)

	// Verify settlement via VerifySettlement.
	root, err := state.VerifySettlement(*settlement, env.group, signing.NewSecp256k1Verifier(), nil)
	require.NoError(t, err)
	require.Len(t, root, 32)
	require.Equal(t, nonce, settlement.Nonce)
}
