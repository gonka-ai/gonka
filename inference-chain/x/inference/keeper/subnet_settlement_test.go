package keeper_test

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"slices"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	dcrdecdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	dcrdsecp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// cosmosAddressFromDcrdKey derives the Cosmos bech32 address from a dcrd private key.
func cosmosAddressFromDcrdKey(key *dcrdsecp.PrivateKey) sdk.AccAddress {
	cosmosPubKey := &secp256k1.PubKey{Key: key.PubKey().SerializeCompressed()}
	return sdk.AccAddress(cosmosPubKey.Address())
}

// signGoEthFormat signs a hash and returns the go-ethereum format [R(32)||S(32)||V(1)].
// dcrd SignCompact returns [V+27(1)||R(32)||S(32)], so we convert.
func signGoEthFormat(key *dcrdsecp.PrivateKey, hash []byte) ([]byte, error) {
	dcrdSig := dcrdecdsa.SignCompact(key, hash, false)
	if len(dcrdSig) != 65 {
		return nil, fmt.Errorf("unexpected sig len %d", len(dcrdSig))
	}
	// dcrd: [V+27(1) || R(32) || S(32)]
	// go-ethereum: [R(32) || S(32) || V(1)]
	goEthSig := make([]byte, 65)
	copy(goEthSig[0:32], dcrdSig[1:33])   // R
	copy(goEthSig[32:64], dcrdSig[33:65])  // S
	goEthSig[64] = dcrdSig[0] - 27         // V
	return goEthSig, nil
}

func buildSettlementTestData(t *testing.T, escrow types.SubnetEscrow, keys []*dcrdsecp.PrivateKey, hostStats []*types.SubnetSettlementHostStats) *types.MsgSettleSubnetEscrow {
	t.Helper()

	entries := make([]*types.SubnetHostStatsProto, len(hostStats))
	for i, hs := range hostStats {
		entries[i] = &types.SubnetHostStatsProto{
			SlotId: hs.SlotId, Missed: hs.Missed, Invalid: hs.Invalid,
			Cost: hs.Cost, RequiredValidations: hs.RequiredValidations,
			CompletedValidations: hs.CompletedValidations,
		}
	}
	slices.SortFunc(entries, func(a, b *types.SubnetHostStatsProto) int {
		return cmp.Compare(a.SlotId, b.SlotId)
	})
	mapProto := &types.SubnetHostStatsMapProto{Entries: entries}
	hostStatsData, err := mapProto.XXX_Marshal(nil, true)
	require.NoError(t, err)
	hostStatsHash := sha256.Sum256(hostStatsData)

	restHash := sha256.Sum256([]byte("rest_data"))

	rootInput := make([]byte, 0, 65)
	rootInput = append(rootInput, hostStatsHash[:]...)
	rootInput = append(rootInput, restHash[:]...)
	rootInput = append(rootInput, 0x02)
	stateRoot := sha256.Sum256(rootInput)

	sigContent := &types.SubnetStateSignatureContent{
		StateRoot: stateRoot[:],
		EscrowId:  fmt.Sprint(escrow.Id),
		Nonce:     42,
	}
	sigData, err := sigContent.XXX_Marshal(nil, true)
	require.NoError(t, err)
	sigHash := sha256.Sum256(sigData)

	var sigs []*types.SubnetSlotSignature
	for i, key := range keys {
		sig, err := signGoEthFormat(key, sigHash[:])
		require.NoError(t, err)
		sigs = append(sigs, &types.SubnetSlotSignature{
			SlotId:    uint32(i),
			Signature: sig,
		})
	}

	return &types.MsgSettleSubnetEscrow{
		Settler:    escrow.Creator,
		EscrowId:   escrow.Id,
		StateRoot:  stateRoot[:],
		Nonce:      42,
		RestHash:   restHash[:],
		HostStats:  hostStats,
		Signatures: sigs,
	}
}

func generateSubnetKeys(t *testing.T, n int) ([]*dcrdsecp.PrivateKey, []string) {
	t.Helper()
	keys := make([]*dcrdsecp.PrivateKey, n)
	slots := make([]string, n)
	for i := 0; i < n; i++ {
		key, err := dcrdsecp.GeneratePrivateKey()
		require.NoError(t, err)
		keys[i] = key
		slots[i] = cosmosAddressFromDcrdKey(key).String()
	}
	return keys, slots
}

func makeHostStats(n int, costPerSlot uint64) []*types.SubnetSettlementHostStats {
	stats := make([]*types.SubnetSettlementHostStats, n)
	for i := 0; i < n; i++ {
		stats[i] = &types.SubnetSettlementHostStats{
			SlotId:               uint32(i),
			Cost:                 costPerSlot,
			RequiredValidations:  10,
			CompletedValidations: 9,
		}
	}
	return stats
}

func TestVerifySubnetSettlement_HappyPath(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateSubnetKeys(t, keeper.SubnetGroupSize)
	escrow := types.SubnetEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.SubnetGroupSize, 100_000_000)
	msg := buildSettlementTestData(t, escrow, keys, hostStats)

	err := keeper.VerifySubnetSettlement(escrow, msg, nil)
	require.NoError(t, err)
}

func TestVerifySubnetSettlement_AlreadySettled(t *testing.T) {
	escrow := types.SubnetEscrow{Id: 1, Creator: "gonka1creator", Settled: true}
	msg := &types.MsgSettleSubnetEscrow{Settler: "gonka1creator", EscrowId: 1}
	err := keeper.VerifySubnetSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already settled")
}

func TestVerifySubnetSettlement_WrongSettler(t *testing.T) {
	escrow := types.SubnetEscrow{Id: 1, Creator: "gonka1creator"}
	msg := &types.MsgSettleSubnetEscrow{Settler: "gonka1wrong", EscrowId: 1}
	err := keeper.VerifySubnetSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not the escrow creator")
}

func TestVerifySubnetSettlement_InsufficientQuorum(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateSubnetKeys(t, keeper.SubnetGroupSize)
	escrow := types.SubnetEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.SubnetGroupSize, 100_000_000)
	msg := buildSettlementTestData(t, escrow, keys, hostStats)
	msg.Signatures = msg.Signatures[:10] // below quorum of 11

	err := keeper.VerifySubnetSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "insufficient quorum")
}

func TestVerifySubnetSettlement_CostExceedsAmount(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateSubnetKeys(t, keeper.SubnetGroupSize)
	escrow := types.SubnetEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 1_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.SubnetGroupSize, 1_000_000_000) // 16 GNK total > 1 GNK
	msg := buildSettlementTestData(t, escrow, keys, hostStats)

	err := keeper.VerifySubnetSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds escrow amount")
}

func TestVerifySubnetSettlement_InvalidSignature(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateSubnetKeys(t, keeper.SubnetGroupSize)

	// Replace slot 0's address with a different key
	wrongKey, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)
	slots[0] = cosmosAddressFromDcrdKey(wrongKey).String()

	escrow := types.SubnetEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.SubnetGroupSize, 100_000_000)
	msg := buildSettlementTestData(t, escrow, keys, hostStats)

	err = keeper.VerifySubnetSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "recovered")
}

func TestVerifySubnetSettlement_WarmKeyAccepted(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	// Generate keys for signing (these are the "warm keys")
	warmKeys, _ := generateSubnetKeys(t, keeper.SubnetGroupSize)

	// Generate different keys for the slot addresses (cold keys)
	coldKeys, coldSlots := generateSubnetKeys(t, keeper.SubnetGroupSize)
	_ = coldKeys // cold keys are not used for signing, only for slot addresses

	escrow := types.SubnetEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: coldSlots,
	}
	hostStats := makeHostStats(keeper.SubnetGroupSize, 100_000_000)

	// Build settlement with warm keys signing
	msg := buildSettlementTestData(t, escrow, warmKeys, hostStats)

	// Without warm key checker, should fail
	err := keeper.VerifySubnetSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "recovered")

	// With warm key checker that accepts all pairings, should pass
	acceptAllWarmKeys := func(granter, grantee string) bool {
		// Accept if grantee is one of the warm key addresses
		for _, wk := range warmKeys {
			if cosmosAddressFromDcrdKey(wk).String() == grantee {
				return true
			}
		}
		return false
	}
	err = keeper.VerifySubnetSettlement(escrow, msg, acceptAllWarmKeys)
	require.NoError(t, err)
}

func TestVerifySubnetSettlement_WarmKeyRejected(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	warmKeys, _ := generateSubnetKeys(t, keeper.SubnetGroupSize)
	_, coldSlots := generateSubnetKeys(t, keeper.SubnetGroupSize)

	escrow := types.SubnetEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: coldSlots,
	}
	hostStats := makeHostStats(keeper.SubnetGroupSize, 100_000_000)
	msg := buildSettlementTestData(t, escrow, warmKeys, hostStats)

	// Warm key checker that rejects all
	rejectAll := func(granter, grantee string) bool {
		return false
	}
	err := keeper.VerifySubnetSettlement(escrow, msg, rejectAll)
	require.Error(t, err)
	require.Contains(t, err.Error(), "recovered")
}

func TestVerifySubnetSettlement_DuplicateSignerMultiSlot(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	// One validator owns all 16 slots
	key, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)
	addr := cosmosAddressFromDcrdKey(key).String()

	slots := make([]string, keeper.SubnetGroupSize)
	for i := range slots {
		slots[i] = addr
	}

	escrow := types.SubnetEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.SubnetGroupSize, 100_000_000)

	// Sign all 16 slots with the same key -- each signature counts as 1 slot vote
	keys := make([]*dcrdsecp.PrivateKey, keeper.SubnetGroupSize)
	for i := range keys {
		keys[i] = key
	}
	msg := buildSettlementTestData(t, escrow, keys, hostStats)

	err = keeper.VerifySubnetSettlement(escrow, msg, nil)
	require.NoError(t, err) // 16 slot votes >= 11 quorum
}

func TestComputeSubnetHostStatsHash_Deterministic(t *testing.T) {
	stats := []*types.SubnetSettlementHostStats{
		{SlotId: 0, Missed: 1, Invalid: 0, Cost: 100, RequiredValidations: 10, CompletedValidations: 9},
		{SlotId: 1, Missed: 0, Invalid: 1, Cost: 200, RequiredValidations: 10, CompletedValidations: 8},
	}

	entries := make([]*types.SubnetHostStatsProto, len(stats))
	for i, hs := range stats {
		entries[i] = &types.SubnetHostStatsProto{
			SlotId: hs.SlotId, Missed: hs.Missed, Invalid: hs.Invalid,
			Cost: hs.Cost, RequiredValidations: hs.RequiredValidations,
			CompletedValidations: hs.CompletedValidations,
		}
	}
	mapProto := &types.SubnetHostStatsMapProto{Entries: entries}
	data1, err := mapProto.XXX_Marshal(nil, true)
	require.NoError(t, err)
	hash1 := sha256.Sum256(data1)

	data2, err := mapProto.XXX_Marshal(nil, true)
	require.NoError(t, err)
	hash2 := sha256.Sum256(data2)

	require.Equal(t, hash1, hash2)
}

func TestVerifySubnetSettlement_DuplicateHostStatsSlotId(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateSubnetKeys(t, keeper.SubnetGroupSize)
	escrow := types.SubnetEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.SubnetGroupSize, 100_000_000)
	// Duplicate slot_id 0 by appending a copy.
	hostStats = append(hostStats, &types.SubnetSettlementHostStats{
		SlotId: 0, Cost: 100_000_000, RequiredValidations: 10, CompletedValidations: 9,
	})
	msg := buildSettlementTestData(t, escrow, keys, hostStats)

	err := keeper.VerifySubnetSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate host_stats slot_id")
}

func TestVerifySubnetSettlement_DuplicateSlotId(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateSubnetKeys(t, keeper.SubnetGroupSize)
	escrow := types.SubnetEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.SubnetGroupSize, 100_000_000)
	msg := buildSettlementTestData(t, escrow, keys, hostStats)

	// Replace all 11 signatures with copies of slot 0's signature
	slot0Sig := msg.Signatures[0]
	dupSigs := make([]*types.SubnetSlotSignature, keeper.SubnetQuorumSlots)
	for i := range dupSigs {
		dupSigs[i] = &types.SubnetSlotSignature{
			SlotId:    slot0Sig.SlotId,
			Signature: slot0Sig.Signature,
		}
	}
	msg.Signatures = dupSigs

	err := keeper.VerifySubnetSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate signature for slot")
}

func TestVerifySubnetSettlement_UnsortedHostStats(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateSubnetKeys(t, keeper.SubnetGroupSize)
	escrow := types.SubnetEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}

	// Create host stats in reverse order
	hostStats := make([]*types.SubnetSettlementHostStats, keeper.SubnetGroupSize)
	for i := 0; i < keeper.SubnetGroupSize; i++ {
		hostStats[i] = &types.SubnetSettlementHostStats{
			SlotId:               uint32(keeper.SubnetGroupSize - 1 - i),
			Cost:                 100_000_000,
			RequiredValidations:  10,
			CompletedValidations: 9,
		}
	}

	msg := buildSettlementTestData(t, escrow, keys, hostStats)

	err := keeper.VerifySubnetSettlement(escrow, msg, nil)
	require.NoError(t, err)
}

func TestVerifySubnetSettlement_ZeroCost(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateSubnetKeys(t, keeper.SubnetGroupSize)
	escrow := types.SubnetEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.SubnetGroupSize, 0) // zero cost

	msg := buildSettlementTestData(t, escrow, keys, hostStats)

	err := keeper.VerifySubnetSettlement(escrow, msg, nil)
	require.NoError(t, err)
}

func TestComputeSubnetHostStatsHash_GoldenValue(t *testing.T) {
	stats := []*types.SubnetSettlementHostStats{
		{SlotId: 0, Missed: 1, Invalid: 0, Cost: 100, RequiredValidations: 10, CompletedValidations: 9},
		{SlotId: 1, Missed: 0, Invalid: 1, Cost: 200, RequiredValidations: 10, CompletedValidations: 8},
	}

	hash, err := keeper.ComputeSubnetHostStatsHash(stats)
	require.NoError(t, err)

	// Fixed golden value -- if this changes, proto marshaling has drifted between
	// the chain-side gogoproto and the subnet-side google-protobuf.
	actual := hex.EncodeToString(hash)
	require.Equal(t, "a3231da94dd50999b9f609263ab7b666431576806437944779c10f8124579fd1", actual, "golden hash mismatch: proto marshaling may have drifted")
}

func TestVerifySubnetSettlement_WrongPhaseRejected(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateSubnetKeys(t, keeper.SubnetGroupSize)
	escrow := types.SubnetEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.SubnetGroupSize, 100_000_000)

	// Build a valid message first
	msg := buildSettlementTestData(t, escrow, keys, hostStats)

	// Recompute state root with wrong phase byte (Active=0x00 instead of Settlement=0x02).
	// This simulates an attacker trying to settle a non-finalized session.
	hostStatsHash, err := keeper.ComputeSubnetHostStatsHash(hostStats)
	require.NoError(t, err)

	rootInput := make([]byte, 0, 65)
	rootInput = append(rootInput, hostStatsHash...)
	rootInput = append(rootInput, msg.RestHash...)
	rootInput = append(rootInput, 0x00) // Active phase, not Settlement
	wrongRoot := sha256.Sum256(rootInput)

	// Re-sign with the wrong state root
	sigContent := &types.SubnetStateSignatureContent{
		StateRoot: wrongRoot[:],
		EscrowId:  fmt.Sprint(escrow.Id),
		Nonce:     msg.Nonce,
	}
	sigData, err := sigContent.XXX_Marshal(nil, true)
	require.NoError(t, err)
	sigHash := sha256.Sum256(sigData)

	var sigs []*types.SubnetSlotSignature
	for i, key := range keys {
		sig, err := signGoEthFormat(key, sigHash[:])
		require.NoError(t, err)
		sigs = append(sigs, &types.SubnetSlotSignature{
			SlotId:    uint32(i),
			Signature: sig,
		})
	}
	msg.StateRoot = wrongRoot[:]
	msg.Signatures = sigs

	err = keeper.VerifySubnetSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "state_root mismatch")
}

func TestSubnetQuorumFor(t *testing.T) {
	tests := []struct {
		groupSize int
		want      int
	}{
		{1, 1},
		{3, 3},
		{8, 6},
		{16, 11},
		{32, 22},
	}
	for _, tc := range tests {
		got := keeper.SubnetQuorumFor(tc.groupSize)
		require.Equal(t, tc.want, got, "SubnetQuorumFor(%d)", tc.groupSize)
	}
}

// Verify signature format conversion roundtrip (go-ethereum <-> dcrd)
func TestSignatureFormatConversion(t *testing.T) {
	key, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)

	hash := sha256.Sum256([]byte("test"))

	// Sign in dcrd format
	dcrdSig := dcrdecdsa.SignCompact(key, hash[:], false)
	require.Len(t, dcrdSig, 65)

	// Convert to go-ethereum format
	goEthSig := make([]byte, 65)
	copy(goEthSig[0:32], dcrdSig[1:33])
	copy(goEthSig[32:64], dcrdSig[33:65])
	goEthSig[64] = dcrdSig[0] - 27

	// Convert back to dcrd format
	roundtrip := make([]byte, 65)
	roundtrip[0] = goEthSig[64] + 27
	copy(roundtrip[1:33], goEthSig[0:32])
	copy(roundtrip[33:65], goEthSig[32:64])

	require.Equal(t, dcrdSig, roundtrip)

	// Verify recovery works with roundtripped sig
	recovered, _, err := dcrdecdsa.RecoverCompact(roundtrip, hash[:])
	require.NoError(t, err)

	// Recovered key should match original
	originalPub := key.PubKey()
	require.True(t, recovered.IsEqual(originalPub))

	// Verify R and S are valid scalars
	r := new(big.Int).SetBytes(goEthSig[0:32])
	s := new(big.Int).SetBytes(goEthSig[32:64])
	require.True(t, r.Sign() > 0)
	require.True(t, s.Sign() > 0)
}
