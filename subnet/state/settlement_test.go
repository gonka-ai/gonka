package state

import (
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"subnet/internal/testutil"
	"subnet/signing"
	"subnet/types"
)

func TestBuildSettlement_MerkleProof(t *testing.T) {
	hostStats := map[uint32]*types.HostStats{
		0: {Cost: 100, RequiredValidations: 2, CompletedValidations: 1},
		1: {Cost: 200, Missed: 1},
	}
	inferences := map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusFinished, ExecutorSlot: 0, ActualCost: 100},
		2: {Status: types.StatusFinished, ExecutorSlot: 1, ActualCost: 200},
	}

	st := types.EscrowState{
		Balance:    9700,
		HostStats:  hostStats,
		Inferences: inferences,
	}

	payload, err := BuildSettlement("escrow-1", st, map[uint32][]byte{0: {1}, 1: {2}}, 10)
	require.NoError(t, err)

	require.Equal(t, "escrow-1", payload.EscrowID)
	require.Equal(t, uint64(10), payload.Nonce)

	// RestHash should match independently computed value.
	restHash, err := ComputeRestHash(st.Balance, st.Inferences, st.WarmKeys)
	require.NoError(t, err)
	require.Equal(t, restHash, payload.RestHash)
}

// buildSignedSettlement creates a settlement payload with valid host signatures
// for use in VerifySettlement tests.
func buildSignedSettlement(t *testing.T, numHosts int) (SettlementPayload, []types.SlotAssignment, signing.Verifier) {
	t.Helper()

	signers := make([]*signing.Secp256k1Signer, numHosts)
	for i := range signers {
		signers[i] = testutil.MustGenerateKey(t)
	}
	group := testutil.MakeGroup(signers)
	verifier := signing.NewSecp256k1Verifier()

	hostStats := map[uint32]*types.HostStats{
		0: {Cost: 100},
		1: {Cost: 200},
	}
	if numHosts > 2 {
		hostStats[2] = &types.HostStats{Cost: 150}
	}

	inferences := map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusFinished, ExecutorSlot: 0, ActualCost: 100},
	}
	st := types.EscrowState{Balance: 9900, HostStats: hostStats, Inferences: inferences}

	escrowID := "escrow-test"
	nonce := uint64(5)

	payload, err := BuildSettlement(escrowID, st, nil, nonce)
	require.NoError(t, err)

	// Recompute state root to sign it.
	hostStatsHash, err := ComputeHostStatsHash(hostStats)
	require.NoError(t, err)
	h := sha256.New()
	h.Write(hostStatsHash)
	h.Write(payload.RestHash)
	h.Write([]byte{uint8(types.PhaseSettlement)})
	stateRoot := h.Sum(nil)

	sigContent := &types.StateSignatureContent{
		StateRoot: stateRoot,
		EscrowId:  escrowID,
		Nonce:     nonce,
	}
	sigData, err := proto.Marshal(sigContent)
	require.NoError(t, err)

	sigs := make(map[uint32][]byte, numHosts)
	for i, s := range signers {
		sig, err := s.Sign(sigData)
		require.NoError(t, err)
		sigs[uint32(i)] = sig
	}
	payload.Signatures = sigs

	return *payload, group, verifier
}

func TestVerifySettlement_Success(t *testing.T) {
	payload, group, verifier := buildSignedSettlement(t, 3)

	root, err := VerifySettlement(payload, group, verifier, nil)
	require.NoError(t, err)
	require.Len(t, root, 32)

	// Independently recompute and compare.
	hostStatsHash, err := ComputeHostStatsHash(payload.HostStats)
	require.NoError(t, err)
	h := sha256.New()
	h.Write(hostStatsHash)
	h.Write(payload.RestHash)
	h.Write([]byte{uint8(types.PhaseSettlement)})
	expected := h.Sum(nil)
	require.Equal(t, expected, root)
}

func TestVerifySettlement_InsufficientSigs(t *testing.T) {
	payload, group, verifier := buildSignedSettlement(t, 3)

	// Keep only 1 of 3 signatures -- below 2/3+1 = 3.
	for slot := range payload.Signatures {
		if slot != 0 {
			delete(payload.Signatures, slot)
		}
	}

	_, err := VerifySettlement(payload, group, verifier, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "insufficient quorum")
}

func TestVerifySettlement_InvalidSig(t *testing.T) {
	payload, group, verifier := buildSignedSettlement(t, 3)

	// Corrupt one signature.
	for slot := range payload.Signatures {
		sig := payload.Signatures[slot]
		sig[0] ^= 0xff
		payload.Signatures[slot] = sig
		break
	}

	_, err := VerifySettlement(payload, group, verifier, nil)
	require.Error(t, err)
}

func TestVerifySettlement_WrongPhase(t *testing.T) {
	// Sign with Finalizing phase (0x01) instead of Settlement (0x02).
	numHosts := 3
	signers := make([]*signing.Secp256k1Signer, numHosts)
	for i := range signers {
		signers[i] = testutil.MustGenerateKey(t)
	}
	group := testutil.MakeGroup(signers)
	verifier := signing.NewSecp256k1Verifier()

	hostStats := map[uint32]*types.HostStats{
		0: {Cost: 100},
		1: {Cost: 200},
		2: {Cost: 150},
	}
	inferences := map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusFinished, ExecutorSlot: 0, ActualCost: 100},
	}
	st := types.EscrowState{Balance: 9900, HostStats: hostStats, Inferences: inferences}

	escrowID := "escrow-test"
	nonce := uint64(5)

	payload, err := BuildSettlement(escrowID, st, nil, nonce)
	require.NoError(t, err)

	// Compute state root with WRONG phase (Finalizing = 0x01).
	hostStatsHash, err := ComputeHostStatsHash(hostStats)
	require.NoError(t, err)
	h := sha256.New()
	h.Write(hostStatsHash)
	h.Write(payload.RestHash)
	h.Write([]byte{uint8(types.PhaseFinalizing)}) // wrong phase
	wrongRoot := h.Sum(nil)

	sigContent := &types.StateSignatureContent{
		StateRoot: wrongRoot,
		EscrowId:  escrowID,
		Nonce:     nonce,
	}
	sigData, err := proto.Marshal(sigContent)
	require.NoError(t, err)

	sigs := make(map[uint32][]byte, numHosts)
	for i, s := range signers {
		sig, err := s.Sign(sigData)
		require.NoError(t, err)
		sigs[uint32(i)] = sig
	}
	payload.Signatures = sigs

	// Verification should fail: recovered addresses won't match group members
	// because VerifySettlement recomputes state root with PhaseSettlement (0x02).
	_, err = VerifySettlement(*payload, group, verifier, nil)
	require.Error(t, err)
}

func TestVerifySettlement_WarmKeySignatures(t *testing.T) {
	// 3 cold signers, 1 warm key for slot 1.
	coldSigners := make([]*signing.Secp256k1Signer, 3)
	for i := range coldSigners {
		coldSigners[i] = testutil.MustGenerateKey(t)
	}
	warmSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(coldSigners)
	verifier := signing.NewSecp256k1Verifier()

	hostStats := map[uint32]*types.HostStats{
		0: {Cost: 100},
		1: {Cost: 200},
		2: {Cost: 150},
	}
	inferences := map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusFinished, ExecutorSlot: 0, ActualCost: 100},
	}
	st := types.EscrowState{Balance: 9900, HostStats: hostStats, Inferences: inferences}

	escrowID := "escrow-warm"
	nonce := uint64(5)
	payload, err := BuildSettlement(escrowID, st, nil, nonce)
	require.NoError(t, err)

	// Recompute state root for signing.
	hostStatsHash, err := ComputeHostStatsHash(hostStats)
	require.NoError(t, err)
	h := sha256.New()
	h.Write(hostStatsHash)
	h.Write(payload.RestHash)
	h.Write([]byte{uint8(types.PhaseSettlement)})
	stateRoot := h.Sum(nil)

	sigContent := &types.StateSignatureContent{
		StateRoot: stateRoot,
		EscrowId:  escrowID,
		Nonce:     nonce,
	}
	sigData, err := proto.Marshal(sigContent)
	require.NoError(t, err)

	// Sign slot 0 and 2 with cold keys, slot 1 with warm key.
	sigs := make(map[uint32][]byte, 3)
	sig0, err := coldSigners[0].Sign(sigData)
	require.NoError(t, err)
	sigs[0] = sig0
	sig1, err := warmSigner.Sign(sigData)
	require.NoError(t, err)
	sigs[1] = sig1
	sig2, err := coldSigners[2].Sign(sigData)
	require.NoError(t, err)
	sigs[2] = sig2
	payload.Signatures = sigs

	warmKeys := map[uint32]string{1: warmSigner.Address()}
	root, err := VerifySettlement(*payload, group, verifier, warmKeys)
	require.NoError(t, err)
	require.Len(t, root, 32)
}

func TestVerifySettlement_WarmKey_NotInMap(t *testing.T) {
	// Sign slot 1 with warm key but pass empty warmKeys -- should fail.
	coldSigners := make([]*signing.Secp256k1Signer, 3)
	for i := range coldSigners {
		coldSigners[i] = testutil.MustGenerateKey(t)
	}
	warmSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(coldSigners)
	verifier := signing.NewSecp256k1Verifier()

	hostStats := map[uint32]*types.HostStats{
		0: {Cost: 100},
		1: {Cost: 200},
		2: {Cost: 150},
	}
	inferences := map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusFinished, ExecutorSlot: 0, ActualCost: 100},
	}
	st := types.EscrowState{Balance: 9900, HostStats: hostStats, Inferences: inferences}

	escrowID := "escrow-warm"
	nonce := uint64(5)
	payload, err := BuildSettlement(escrowID, st, nil, nonce)
	require.NoError(t, err)

	hostStatsHash, err := ComputeHostStatsHash(hostStats)
	require.NoError(t, err)
	hh := sha256.New()
	hh.Write(hostStatsHash)
	hh.Write(payload.RestHash)
	hh.Write([]byte{uint8(types.PhaseSettlement)})
	stateRoot := hh.Sum(nil)

	sigContent := &types.StateSignatureContent{
		StateRoot: stateRoot,
		EscrowId:  escrowID,
		Nonce:     nonce,
	}
	sigData, err := proto.Marshal(sigContent)
	require.NoError(t, err)

	sigs := make(map[uint32][]byte, 3)
	for i, s := range coldSigners {
		sig, err := s.Sign(sigData)
		require.NoError(t, err)
		sigs[uint32(i)] = sig
	}
	// Replace slot 1 sig with warm key sig.
	warmSig, err := warmSigner.Sign(sigData)
	require.NoError(t, err)
	sigs[1] = warmSig
	payload.Signatures = sigs

	_, err = VerifySettlement(*payload, group, verifier, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in group")
}
