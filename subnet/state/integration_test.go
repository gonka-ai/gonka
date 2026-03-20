package state

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"subnet/internal/testutil"
	"subnet/signing"
	"subnet/types"
)

// TestFullSession_HappyPath runs a complete session with 5 hosts and 15 inferences
// (3 full rounds of round-robin). Each diff includes MsgStartInference plus
// accumulated MsgConfirmStart/MsgFinishInference from previous rounds.
// State roots are signed by hosts. Verifies final host stats, balance, and signatures.
func TestFullSession_HappyPath(t *testing.T) {
	const numHosts = 5
	const numInferences = 15

	// Generate keys.
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()

	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	escrowID := "escrow-integration"
	initialBalance := uint64(100000)

	sm := NewStateMachine(escrowID, config, group, initialBalance, user.Address(), verifier)

	// Track pending operations from previous diffs that need to be included.
	type pendingConfirm struct {
		inferenceID uint64
		executorIdx int
	}
	type pendingFinish struct {
		inferenceID uint64
		executorIdx int
	}

	var pendingConfirms []pendingConfirm
	var pendingFinishes []pendingFinish

	// State root signatures: nonce -> slot_id -> signature.
	stateRootSigs := make(map[uint64]map[uint32][]byte)

	nonce := uint64(0)

	for infID := uint64(1); infID <= numInferences; infID++ {
		executorIdx := int(infID % uint64(numHosts))

		// Build txs for this diff.
		var txs []*types.SubnetTx

		// Include accumulated ConfirmStart txs.
		for _, pc := range pendingConfirms {
			execSig := testutil.SignExecutorReceipt(t, hosts[pc.executorIdx], escrowID,
				pc.inferenceID, []byte("prompt"), "llama", 100, 50, int64(pc.inferenceID)*1000, int64(pc.inferenceID)*1000)
			txs = append(txs, txConfirm(&types.MsgConfirmStart{
				InferenceId: pc.inferenceID,
				ExecutorSig: execSig,
				ConfirmedAt: int64(pc.inferenceID) * 1000,
			}))
		}
		pendingConfirms = nil

		// Include accumulated FinishInference txs.
		for _, pf := range pendingFinishes {
			finishMsg := &types.MsgFinishInference{
				InferenceId:  pf.inferenceID,
				ResponseHash: []byte("response"),
				InputTokens:  80,
				OutputTokens: 40,
				ExecutorSlot: uint32(pf.executorIdx),
				EscrowId:     escrowID,
			}
			proposerSig := testutil.SignProposerTx(t, hosts[pf.executorIdx], finishMsg)
			finishMsg.ProposerSig = proposerSig
			txs = append(txs, txFinish(finishMsg))
		}
		pendingFinishes = nil

		// New MsgStartInference.
		txs = append(txs, txStart(&types.MsgStartInference{
			InferenceId: infID,
			PromptHash:  []byte("prompt"),
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   int64(infID) * 1000,
		}))

		nonce++
		diff := testutil.SignDiff(t, user, escrowID, nonce, txs)
		stateRoot, err := sm.ApplyDiff(diff)
		require.NoError(t, err, "diff %d", nonce)

		// The host at position (nonce-1) % numHosts signs the state root.
		signerIdx := int((nonce - 1) % numHosts)
		stateSignContent := &types.StateSignatureContent{
			StateRoot: stateRoot,
			EscrowId:  escrowID,
			Nonce:     nonce,
		}
		sigData, err := proto.Marshal(stateSignContent)
		require.NoError(t, err)
		sig, err := hosts[signerIdx].Sign(sigData)
		require.NoError(t, err)

		if stateRootSigs[nonce] == nil {
			stateRootSigs[nonce] = make(map[uint32][]byte)
		}
		stateRootSigs[nonce][uint32(signerIdx)] = sig

		// Queue confirm and finish for next diffs.
		pendingConfirms = append(pendingConfirms, pendingConfirm{
			inferenceID: infID,
			executorIdx: executorIdx,
		})
		pendingFinishes = append(pendingFinishes, pendingFinish{
			inferenceID: infID,
			executorIdx: executorIdx,
		})
	}

	// Apply remaining confirms and finishes.
	var finalTxs []*types.SubnetTx
	for _, pc := range pendingConfirms {
		execSig := testutil.SignExecutorReceipt(t, hosts[pc.executorIdx], escrowID,
			pc.inferenceID, []byte("prompt"), "llama", 100, 50, int64(pc.inferenceID)*1000, int64(pc.inferenceID)*1000)
		finalTxs = append(finalTxs, txConfirm(&types.MsgConfirmStart{
			InferenceId: pc.inferenceID,
			ExecutorSig: execSig,
			ConfirmedAt: int64(pc.inferenceID) * 1000,
		}))
	}
	for _, pf := range pendingFinishes {
		finishMsg := &types.MsgFinishInference{
			InferenceId:  pf.inferenceID,
			ResponseHash: []byte("response"),
			InputTokens:  80,
			OutputTokens: 40,
			ExecutorSlot: uint32(pf.executorIdx),
			EscrowId:     escrowID,
		}
		proposerSig := testutil.SignProposerTx(t, hosts[pf.executorIdx], finishMsg)
		finishMsg.ProposerSig = proposerSig
		finalTxs = append(finalTxs, txFinish(finishMsg))
	}

	nonce++
	diff := testutil.SignDiff(t, user, escrowID, nonce, finalTxs)
	finalStateRoot, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Sign final state with all hosts.
	finalSigs := make(map[uint32][]byte)
	for i, host := range hosts {
		stateSignContent := &types.StateSignatureContent{
			StateRoot: finalStateRoot,
			EscrowId:  escrowID,
			Nonce:     nonce,
		}
		sigData, err := proto.Marshal(stateSignContent)
		require.NoError(t, err)
		sig, err := host.Sign(sigData)
		require.NoError(t, err)
		finalSigs[uint32(i)] = sig
	}

	// Verify all inferences finished.
	state := sm.SnapshotState()
	require.Len(t, state.Inferences, numInferences)
	for id, rec := range state.Inferences {
		require.Equal(t, types.StatusFinished, rec.Status, "inference %d", id)
	}

	// Verify host stats: each host executed 3 inferences.
	// Executor for ID i: i % 5. IDs 1-15:
	// slot 0: IDs 5, 10, 15 -> 3 inferences
	// slot 1: IDs 1, 6, 11  -> 3 inferences
	// slot 2: IDs 2, 7, 12  -> 3 inferences
	// slot 3: IDs 3, 8, 13  -> 3 inferences
	// slot 4: IDs 4, 9, 14  -> 3 inferences
	actualCostPerInference := uint64(120) // (80+40)*1
	for slot := uint32(0); slot < numHosts; slot++ {
		hs := state.HostStats[slot]
		require.Equal(t, uint64(3)*actualCostPerInference, hs.Cost,
			"slot %d cost", slot)
		require.Equal(t, uint32(0), hs.Missed, "slot %d missed", slot)
		require.Equal(t, uint32(0), hs.Invalid, "slot %d invalid", slot)
	}

	// Verify balance = initial - total_cost.
	totalCost := uint64(numInferences) * actualCostPerInference
	require.Equal(t, initialBalance-totalCost, state.Balance)

	// Verify 5/5 signatures on final state (>= 2/3+).
	require.Len(t, finalSigs, numHosts)
	for slot, sig := range finalSigs {
		stateSignContent := &types.StateSignatureContent{
			StateRoot: finalStateRoot,
			EscrowId:  escrowID,
			Nonce:     nonce,
		}
		sigData, err := proto.Marshal(stateSignContent)
		require.NoError(t, err)
		recovered, err := verifier.RecoverAddress(sigData, sig)
		require.NoError(t, err)
		require.Equal(t, hosts[slot].Address(), recovered, "slot %d sig", slot)
	}

	// Verify Merkle structure.
	hostStatsHash, err := ComputeHostStatsHash(state.HostStats)
	require.NoError(t, err)
	restHash, err := ComputeRestHash(state.Balance, state.Inferences, state.WarmKeys)
	require.NoError(t, err)
	recomputedRoot, err := ComputeStateRoot(state.Balance, state.HostStats, state.Inferences, state.Phase, state.WarmKeys)
	require.NoError(t, err)
	require.Equal(t, finalStateRoot, recomputedRoot)

	// Settlement payload verification: mainnet would verify
	// hash(hostStatsHash || restHash) == stateRoot.
	require.NotEmpty(t, hostStatsHash)
	require.NotEmpty(t, restHash)
}
