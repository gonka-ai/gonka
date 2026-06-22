package keeper_test

import (
	"crypto/sha256"
	"fmt"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	dcrdsecp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// TestFinding2_DistinctValidatorQuorum is the regression guard for the
// distinct-validator quorum fix. One validator can own most slots (weighted
// sampling with replacement) and the signed payload omits slot_id, so before
// the fix it could meet quorum by replaying one signature. Here 16 slots map to
// 6 distinct validators (quorum 5): the attacker's 11 slots count as ONE vote
// (rejected), while attacker + 4 honest = 5 distinct votes settles.
func TestFinding2_DistinctValidatorQuorum(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	const attackerSlots = 11 // slots 0..10
	const honestCount = 5    // slots 11..15

	attacker, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)
	attackerAddr := cosmosAddressFromDcrdKey(attacker).String()

	honestKeys := make([]*dcrdsecp.PrivateKey, honestCount)
	slots := make([]string, keeper.DevshardGroupSize) // 16
	for i := 0; i < attackerSlots; i++ {
		slots[i] = attackerAddr // one validator owns a majority of slots
	}
	for i := 0; i < honestCount; i++ {
		k, err := dcrdsecp.GeneratePrivateKey()
		require.NoError(t, err)
		honestKeys[i] = k
		slots[attackerSlots+i] = cosmosAddressFromDcrdKey(k).String()
	}

	escrow := types.DevshardEscrow{
		Id:      1,
		Creator: "gonka1creator",
		Amount:  7_000_000_000,
		Slots:   slots,
	}
	hostStats := makeHostStats(keeper.DevshardGroupSize, 1_000_000)

	distinct := map[string]bool{}
	for _, a := range slots {
		distinct[a] = true
	}
	require.Equal(t, 6, len(distinct), "escrow resolves to 6 distinct validators")
	require.Equal(t, 5, keeper.DevshardQuorumFor(len(distinct)), "distinct-validator quorum is 5")

	// Baseline message yields the correct state_root and the slot-independent
	// sigHash; the attacker signs it once (SlotId 0).
	base := buildSettlementTestData(t, escrow, []*dcrdsecp.PrivateKey{attacker}, hostStats, 0)
	attackerSig := base.Signatures[0].Signature
	require.Len(t, attackerSig, 65)

	// Recompute the exact sigHash the chain verifies against so we can co-sign
	// with the honest validators.
	sigContent := &types.DevshardStateSignatureContent{
		StateRoot: base.StateRoot,
		EscrowId:  fmt.Sprint(escrow.Id),
		Nonce:     base.Nonce,
	}
	sigData, err := sigContent.XXX_Marshal(nil, true)
	require.NoError(t, err)
	sigHash := sha256.Sum256(sigData)

	// --- Attack: attacker reuses its single signature across all 11 owned slots.
	forged := make([]*types.DevshardSlotSignature, 0, attackerSlots)
	for i := 0; i < attackerSlots; i++ {
		forged = append(forged, &types.DevshardSlotSignature{SlotId: uint32(i), Signature: attackerSig})
	}
	base.Signatures = forged
	err = keeper.VerifyDevshardSettlement(escrow, base, testDevshardEscrowParams(), nil)
	require.Error(t, err,
		"REGRESSION: a single validator owning 11 slots must NOT meet quorum by replaying one signature")
	require.Contains(t, err.Error(), "insufficient quorum",
		"single validator is one distinct vote, below the 5 distinct-validator quorum")

	// --- Liveness: attacker (11 slots) + 4 honest distinct validators = 5 votes -> accepted.
	live := make([]*types.DevshardSlotSignature, 0, attackerSlots+4)
	for i := 0; i < attackerSlots; i++ {
		live = append(live, &types.DevshardSlotSignature{SlotId: uint32(i), Signature: attackerSig})
	}
	for i := 0; i < 4; i++ { // honest validators own slots 11..14
		hs, err := signGoEthFormat(honestKeys[i], sigHash[:])
		require.NoError(t, err)
		live = append(live, &types.DevshardSlotSignature{SlotId: uint32(attackerSlots + i), Signature: hs})
	}
	base.Signatures = live
	err = keeper.VerifyDevshardSettlement(escrow, base, testDevshardEscrowParams(), nil)
	require.NoError(t, err, "a genuine 5-distinct-validator quorum must still settle")
}
