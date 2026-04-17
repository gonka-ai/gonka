package keeper_test

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"slices"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	dcrdsecp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	dcrdecdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

const settlementVersion = "dev"

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
	copy(goEthSig[32:64], dcrdSig[33:65]) // S
	goEthSig[64] = dcrdSig[0] - 27        // V
	return goEthSig, nil
}

func buildSettlementTestData(
	t *testing.T,
	escrow types.DevshardEscrow,
	keys []*dcrdsecp.PrivateKey,
	hostStats []*types.DevshardSettlementHostStats,
	fees uint64,
) *types.MsgSettleDevshardEscrow {
	return buildSettlementTestDataWithNonce(t, escrow, keys, hostStats, fees, 42)
}

func buildSettlementTestDataWithNonce(
	t *testing.T,
	escrow types.DevshardEscrow,
	keys []*dcrdsecp.PrivateKey,
	hostStats []*types.DevshardSettlementHostStats,
	fees uint64,
	nonce uint64,
) *types.MsgSettleDevshardEscrow {
	t.Helper()

	entries := make([]*types.DevshardHostStatsProto, len(hostStats))
	for i, hs := range hostStats {
		entries[i] = &types.DevshardHostStatsProto{
			SlotId: hs.SlotId, Missed: hs.Missed, Invalid: hs.Invalid,
			Cost: hs.Cost, RequiredValidations: hs.RequiredValidations,
			CompletedValidations: hs.CompletedValidations,
		}
	}
	slices.SortFunc(entries, func(a, b *types.DevshardHostStatsProto) int {
		return cmp.Compare(a.SlotId, b.SlotId)
	})
	mapProto := &types.DevshardHostStatsMapProto{Entries: entries}
	hostStatsData, err := mapProto.XXX_Marshal(nil, true)
	require.NoError(t, err)
	hostStatsHash := sha256.Sum256(hostStatsData)

	restHash := sha256.Sum256([]byte("rest_data"))
	feesBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(feesBytes, fees)
	versionHash := sha256.Sum256([]byte(settlementVersion))

	rootInput := make([]byte, 0, 105)
	rootInput = append(rootInput, hostStatsHash[:]...)
	rootInput = append(rootInput, feesBytes...)
	rootInput = append(rootInput, restHash[:]...)
	rootInput = append(rootInput, versionHash[:]...)
	rootInput = append(rootInput, 0x02)
	stateRoot := sha256.Sum256(rootInput)

	sigContent := &types.DevshardStateSignatureContent{
		StateRoot: stateRoot[:],
		EscrowId:  fmt.Sprint(escrow.Id),
		Nonce:     nonce,
	}
	sigData, err := sigContent.XXX_Marshal(nil, true)
	require.NoError(t, err)
	sigHash := sha256.Sum256(sigData)

	var sigs []*types.DevshardSlotSignature
	for i, key := range keys {
		sig, err := signGoEthFormat(key, sigHash[:])
		require.NoError(t, err)
		sigs = append(sigs, &types.DevshardSlotSignature{
			SlotId:    uint32(i),
			Signature: sig,
		})
	}

	return &types.MsgSettleDevshardEscrow{
		Settler:    escrow.Creator,
		EscrowId:   escrow.Id,
		Version:    settlementVersion,
		StateRoot:  stateRoot[:],
		Nonce:      nonce,
		Fees:       fees,
		RestHash:   restHash[:],
		HostStats:  hostStats,
		Signatures: sigs,
	}
}

func generateDevshardKeys(t *testing.T, n int) ([]*dcrdsecp.PrivateKey, []string) {
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

func makeHostStats(n int, costPerSlot uint64) []*types.DevshardSettlementHostStats {
	stats := make([]*types.DevshardSettlementHostStats, n)
	for i := 0; i < n; i++ {
		stats[i] = &types.DevshardSettlementHostStats{
			SlotId:               uint32(i),
			Cost:                 costPerSlot,
			RequiredValidations:  10,
			CompletedValidations: 9,
		}
	}
	return stats
}

func TestVerifyDevshardSettlement_HappyPath(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	escrow := types.DevshardEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.DevshardGroupSize, 100_000_000)
	msg := buildSettlementTestData(t, escrow, keys, hostStats, 0)

	err := keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.NoError(t, err)
}

func TestVerifyDevshardSettlement_AlreadySettled(t *testing.T) {
	escrow := types.DevshardEscrow{Id: 1, Creator: "gonka1creator", Settled: true}
	msg := &types.MsgSettleDevshardEscrow{Settler: "gonka1creator", EscrowId: 1}
	err := keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already settled")
}

func TestVerifyDevshardSettlement_WrongSettler(t *testing.T) {
	escrow := types.DevshardEscrow{Id: 1, Creator: "gonka1creator"}
	msg := &types.MsgSettleDevshardEscrow{Settler: "gonka1wrong", EscrowId: 1}
	err := keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not the escrow creator")
}

func TestVerifyDevshardSettlement_VersionTooLong(t *testing.T) {
	escrow := types.DevshardEscrow{Id: 1, Creator: "gonka1creator"}
	msg := &types.MsgSettleDevshardEscrow{Settler: "gonka1creator", EscrowId: 1, Version: string(make([]byte, 129))}
	err := keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "version exceeds maximum length")
}

func TestVerifyDevshardSettlement_InsufficientQuorum(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	escrow := types.DevshardEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.DevshardGroupSize, 100_000_000)
	msg := buildSettlementTestData(t, escrow, keys, hostStats, 0)
	msg.Signatures = msg.Signatures[:10] // below quorum of 11

	err := keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "insufficient quorum")
}

func TestVerifyDevshardSettlement_CostExceedsAmount(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	escrow := types.DevshardEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 1_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.DevshardGroupSize, 1_000_000_000) // 16 GNK total > 1 GNK
	msg := buildSettlementTestData(t, escrow, keys, hostStats, 0)

	err := keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds escrow amount")
}

func TestVerifyDevshardSettlement_FeesExceedAmount(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	escrow := types.DevshardEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 1_000_000_000, Slots: slots,
	}
	// cost + fees = 1_000_000_001
	hostStats := makeHostStats(keeper.DevshardGroupSize, 50_000_000) // total 800M
	msg := buildSettlementTestData(t, escrow, keys, hostStats, 200_000_001)

	err := keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds escrow amount")
}

func TestVerifyDevshardSettlement_NonceExceedsLimit(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	escrow := types.DevshardEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.DevshardGroupSize, 100_000_000)
	msg := buildSettlementTestDataWithNonce(t, escrow, keys, hostStats, 0, keeper.DevshardMaxNonce+1)

	err := keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum")
}

func TestVerifyDevshardSettlement_MissedExceedsAssignedPerSlot(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	escrow := types.DevshardEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.DevshardGroupSize, 100_000_000)
	hostStats[0].Missed = 3
	msg := buildSettlementTestDataWithNonce(t, escrow, keys, hostStats, 0, 32)

	err := keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missed count")
}

func TestVerifyDevshardSettlement_InvalidExceedsCompletedPerSlot(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	escrow := types.DevshardEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.DevshardGroupSize, 100_000_000)
	hostStats[0].Missed = 1
	hostStats[0].Invalid = 2
	msg := buildSettlementTestDataWithNonce(t, escrow, keys, hostStats, 0, 32)

	err := keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid count")
}

func TestVerifyDevshardSettlement_RemainderSlotMissedAllowed(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	escrow := types.DevshardEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.DevshardGroupSize, 100_000_000)
	hostStats[1].Missed = 2 // nonce 19 => slot 1 is one of the remainder slots
	msg := buildSettlementTestDataWithNonce(t, escrow, keys, hostStats, 0, 19)

	err := keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.NoError(t, err)
}

func TestVerifyDevshardSettlement_NonRemainderSlotMissedRejected(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	escrow := types.DevshardEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.DevshardGroupSize, 100_000_000)
	hostStats[0].Missed = 2 // nonce 19 => slot 0 only gets nonce 16
	msg := buildSettlementTestDataWithNonce(t, escrow, keys, hostStats, 0, 19)

	err := keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missed count")
}

func TestVerifyDevshardSettlement_InvalidSignature(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)

	// Replace slot 0's address with a different key
	wrongKey, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)
	slots[0] = cosmosAddressFromDcrdKey(wrongKey).String()

	escrow := types.DevshardEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.DevshardGroupSize, 100_000_000)
	msg := buildSettlementTestData(t, escrow, keys, hostStats, 0)

	err = keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "recovered")
}

func TestVerifyDevshardSettlement_WarmKeyAccepted(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	// Generate keys for signing (these are the "warm keys")
	warmKeys, _ := generateDevshardKeys(t, keeper.DevshardGroupSize)

	// Generate different keys for the slot addresses (cold keys)
	coldKeys, coldSlots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	_ = coldKeys // cold keys are not used for signing, only for slot addresses

	escrow := types.DevshardEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: coldSlots,
	}
	hostStats := makeHostStats(keeper.DevshardGroupSize, 100_000_000)

	// Build settlement with warm keys signing
	msg := buildSettlementTestData(t, escrow, warmKeys, hostStats, 0)

	// Without warm key checker, should fail
	err := keeper.VerifyDevshardSettlement(escrow, msg, nil)
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
	err = keeper.VerifyDevshardSettlement(escrow, msg, acceptAllWarmKeys)
	require.NoError(t, err)
}

func TestVerifyDevshardSettlement_WarmKeyRejected(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	warmKeys, _ := generateDevshardKeys(t, keeper.DevshardGroupSize)
	_, coldSlots := generateDevshardKeys(t, keeper.DevshardGroupSize)

	escrow := types.DevshardEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: coldSlots,
	}
	hostStats := makeHostStats(keeper.DevshardGroupSize, 100_000_000)
	msg := buildSettlementTestData(t, escrow, warmKeys, hostStats, 0)

	// Warm key checker that rejects all
	rejectAll := func(granter, grantee string) bool {
		return false
	}
	err := keeper.VerifyDevshardSettlement(escrow, msg, rejectAll)
	require.Error(t, err)
	require.Contains(t, err.Error(), "recovered")
}

func TestVerifyDevshardSettlement_DuplicateSignerMultiSlot(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	// One validator owns all 16 slots
	key, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)
	addr := cosmosAddressFromDcrdKey(key).String()

	slots := make([]string, keeper.DevshardGroupSize)
	for i := range slots {
		slots[i] = addr
	}

	escrow := types.DevshardEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.DevshardGroupSize, 100_000_000)

	// Sign all 16 slots with the same key -- each signature counts as 1 slot vote
	keys := make([]*dcrdsecp.PrivateKey, keeper.DevshardGroupSize)
	for i := range keys {
		keys[i] = key
	}
	msg := buildSettlementTestData(t, escrow, keys, hostStats, 0)

	err = keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.NoError(t, err) // 16 slot votes >= 11 quorum
}

func TestComputeDevshardHostStatsHash_Deterministic(t *testing.T) {
	stats := []*types.DevshardSettlementHostStats{
		{SlotId: 0, Missed: 1, Invalid: 0, Cost: 100, RequiredValidations: 10, CompletedValidations: 9},
		{SlotId: 1, Missed: 0, Invalid: 1, Cost: 200, RequiredValidations: 10, CompletedValidations: 8},
	}

	entries := make([]*types.DevshardHostStatsProto, len(stats))
	for i, hs := range stats {
		entries[i] = &types.DevshardHostStatsProto{
			SlotId: hs.SlotId, Missed: hs.Missed, Invalid: hs.Invalid,
			Cost: hs.Cost, RequiredValidations: hs.RequiredValidations,
			CompletedValidations: hs.CompletedValidations,
		}
	}
	mapProto := &types.DevshardHostStatsMapProto{Entries: entries}
	data1, err := mapProto.XXX_Marshal(nil, true)
	require.NoError(t, err)
	hash1 := sha256.Sum256(data1)

	data2, err := mapProto.XXX_Marshal(nil, true)
	require.NoError(t, err)
	hash2 := sha256.Sum256(data2)

	require.Equal(t, hash1, hash2)
}

func TestVerifyDevshardSettlement_DuplicateHostStatsSlotId(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	escrow := types.DevshardEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.DevshardGroupSize, 100_000_000)
	// Duplicate slot_id 0 by appending a copy.
	hostStats = append(hostStats, &types.DevshardSettlementHostStats{
		SlotId: 0, Cost: 100_000_000, RequiredValidations: 10, CompletedValidations: 9,
	})
	msg := buildSettlementTestData(t, escrow, keys, hostStats, 0)

	err := keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate host_stats slot_id")
}

func TestVerifyDevshardSettlement_DuplicateSlotId(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	escrow := types.DevshardEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.DevshardGroupSize, 100_000_000)
	msg := buildSettlementTestData(t, escrow, keys, hostStats, 0)

	// Replace all 11 signatures with copies of slot 0's signature
	slot0Sig := msg.Signatures[0]
	dupSigs := make([]*types.DevshardSlotSignature, keeper.DevshardQuorumSlots)
	for i := range dupSigs {
		dupSigs[i] = &types.DevshardSlotSignature{
			SlotId:    slot0Sig.SlotId,
			Signature: slot0Sig.Signature,
		}
	}
	msg.Signatures = dupSigs

	err := keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate signature for slot")
}

func TestVerifyDevshardSettlement_UnsortedHostStats(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	escrow := types.DevshardEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}

	// Create host stats in reverse order
	hostStats := make([]*types.DevshardSettlementHostStats, keeper.DevshardGroupSize)
	for i := 0; i < keeper.DevshardGroupSize; i++ {
		hostStats[i] = &types.DevshardSettlementHostStats{
			SlotId:               uint32(keeper.DevshardGroupSize - 1 - i),
			Cost:                 100_000_000,
			RequiredValidations:  10,
			CompletedValidations: 9,
		}
	}

	msg := buildSettlementTestData(t, escrow, keys, hostStats, 0)

	err := keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.NoError(t, err)
}

func TestVerifyDevshardSettlement_ZeroCost(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	escrow := types.DevshardEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.DevshardGroupSize, 0) // zero cost

	msg := buildSettlementTestData(t, escrow, keys, hostStats, 0)

	err := keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.NoError(t, err)
}

func TestComputeDevshardHostStatsHash_GoldenValue(t *testing.T) {
	stats := []*types.DevshardSettlementHostStats{
		{SlotId: 0, Missed: 1, Invalid: 0, Cost: 100, RequiredValidations: 10, CompletedValidations: 9},
		{SlotId: 1, Missed: 0, Invalid: 1, Cost: 200, RequiredValidations: 10, CompletedValidations: 8},
	}

	hash, err := keeper.ComputeDevshardHostStatsHash(stats)
	require.NoError(t, err)

	// Fixed golden value -- if this changes, proto marshaling has drifted between
	// the chain-side gogoproto and the devshard-side google-protobuf.
	actual := hex.EncodeToString(hash)
	require.Equal(t, "a3231da94dd50999b9f609263ab7b666431576806437944779c10f8124579fd1", actual, "golden hash mismatch: proto marshaling may have drifted")
}

func TestVerifyDevshardSettlement_WrongPhaseRejected(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	escrow := types.DevshardEscrow{
		Id: 1, Creator: "gonka1creator", Amount: 7_000_000_000, Slots: slots,
	}
	hostStats := makeHostStats(keeper.DevshardGroupSize, 100_000_000)

	// Build a valid message first
	msg := buildSettlementTestData(t, escrow, keys, hostStats, 0)

	// Recompute state root with wrong phase byte (Active=0x00 instead of Settlement=0x02).
	// This simulates an attacker trying to settle a non-finalized session.
	hostStatsHash, err := keeper.ComputeDevshardHostStatsHash(hostStats)
	require.NoError(t, err)
	feesBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(feesBytes, msg.Fees)
	versionHash := sha256.Sum256([]byte(msg.Version))

	rootInput := make([]byte, 0, 105)
	rootInput = append(rootInput, hostStatsHash...)
	rootInput = append(rootInput, feesBytes...)
	rootInput = append(rootInput, msg.RestHash...)
	rootInput = append(rootInput, versionHash[:]...)
	rootInput = append(rootInput, 0x00) // Active phase, not Settlement
	wrongRoot := sha256.Sum256(rootInput)

	// Re-sign with the wrong state root
	sigContent := &types.DevshardStateSignatureContent{
		StateRoot: wrongRoot[:],
		EscrowId:  fmt.Sprint(escrow.Id),
		Nonce:     msg.Nonce,
	}
	sigData, err := sigContent.XXX_Marshal(nil, true)
	require.NoError(t, err)
	sigHash := sha256.Sum256(sigData)

	var sigs []*types.DevshardSlotSignature
	for i, key := range keys {
		sig, err := signGoEthFormat(key, sigHash[:])
		require.NoError(t, err)
		sigs = append(sigs, &types.DevshardSlotSignature{
			SlotId:    uint32(i),
			Signature: sig,
		})
	}
	msg.StateRoot = wrongRoot[:]
	msg.Signatures = sigs

	err = keeper.VerifyDevshardSettlement(escrow, msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "state_root mismatch")
}

func TestDevshardQuorumFor(t *testing.T) {
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
		got := keeper.DevshardQuorumFor(tc.groupSize)
		require.Equal(t, tc.want, got, "DevshardQuorumFor(%d)", tc.groupSize)
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
