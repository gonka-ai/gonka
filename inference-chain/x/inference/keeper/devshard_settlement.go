package keeper

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/bits"
	"slices"

	cosmossecp "github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/productscience/inference/x/inference/types"
)

const (
	DevshardGroupSize   = 16
	DevshardQuorumSlots = 2*DevshardGroupSize/3 + 1
	// DevshardSettlementPhase is the phase byte appended to the state root preimage.
	// The chain hardcodes 0x02 (Settlement) so only fully-finalized devshard states
	// can pass verification. States at phase Active (0x00) or Finalizing (0x01)
	// produce a different hash and are rejected by the state_root mismatch check.
	DevshardSettlementPhase = byte(0x02)
)

// DevshardQuorumFor returns the minimum slot votes required for a given group size.
func DevshardQuorumFor(groupSize int) int {
	return 2*groupSize/3 + 1
}

// devshardAssignedUpperBoundForSlot returns the maximum number of inference IDs
// that could have been assigned to a slot, based on devshard's executor routing.
// This mirrors the devshard-side contract in `devshard/user/user.go`, where
// diffs advance nonce from `latest+1`, and `devshard/state/machine.go`, where
// `inference_id == nonce` is routed as `group[inference_id % len(group)]`.
// Because nonce 0 is never used for a real inference diff, slot 0 first
// receives work at nonce `slotCount`, while slots 1..slotCount-1 first receive
// work at their matching nonce.
func devshardAssignedUpperBoundForSlot(latestNonce, slotCount uint64, slotID uint32) (uint64, error) {
	if slotCount == 0 {
		return 0, fmt.Errorf("slot count cannot be zero")
	}
	if uint64(slotID) >= slotCount {
		return 0, fmt.Errorf("slot %d out of range for slot count %d", slotID, slotCount)
	}

	firstAssignedNonce := uint64(slotID)
	if slotID == 0 {
		firstAssignedNonce = slotCount
	}
	if latestNonce < firstAssignedNonce {
		return 0, nil
	}
	return 1 + (latestNonce-firstAssignedNonce)/slotCount, nil
}

// WarmKeyChecker returns true if grantee has an authz grant from granter.
type WarmKeyChecker func(granter, grantee string) bool

// VerifyDevshardSettlement verifies settlement proof: state root, signatures, quorum, cost.
// If isWarmKey is non-nil, mismatched signatures are checked against authz grants.
func VerifyDevshardSettlement(escrow types.DevshardEscrow, msg *types.MsgSettleDevshardEscrow, maxNonce uint32, isWarmKey WarmKeyChecker) error {
	if escrow.Settled {
		return fmt.Errorf("escrow %d already settled", escrow.Id)
	}
	if msg.Settler != escrow.Creator {
		return fmt.Errorf("settler %s is not the escrow creator %s", msg.Settler, escrow.Creator)
	}
	if msg.Version == "" {
		return fmt.Errorf("version is required")
	}
	if msg.Nonce > uint64(maxNonce) {
		return fmt.Errorf("nonce %d exceeds maximum %d", msg.Nonce, maxNonce)
	}
	const maxVersionLength = 128
	if len(msg.Version) > maxVersionLength {
		return fmt.Errorf("version exceeds maximum length of %d", maxVersionLength)
	}

	// Recompute host_stats_hash
	hostStatsHash, err := ComputeDevshardHostStatsHash(msg.HostStats)
	if err != nil {
		return fmt.Errorf("failed to compute host stats hash: %w", err)
	}

	// Verify state_root = sha256(host_stats_hash || fees_be || rest_hash || version_hash || 0x02)
	feesBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(feesBytes, msg.Fees)
	versionHash := sha256.Sum256([]byte(msg.Version))
	rootInput := make([]byte, 0, len(hostStatsHash)+len(feesBytes)+len(msg.RestHash)+len(versionHash)+1)
	rootInput = append(rootInput, hostStatsHash...)
	rootInput = append(rootInput, feesBytes...)
	rootInput = append(rootInput, msg.RestHash...)
	rootInput = append(rootInput, versionHash[:]...)
	rootInput = append(rootInput, DevshardSettlementPhase)
	expectedRoot := sha256.Sum256(rootInput)
	if len(msg.StateRoot) != 32 {
		return fmt.Errorf("state_root must be 32 bytes, got %d", len(msg.StateRoot))
	}
	if !bytes.Equal(expectedRoot[:], msg.StateRoot) {
		return fmt.Errorf("state_root mismatch")
	}

	// Build signature data using deterministic proto marshal
	sigContent := &types.DevshardStateSignatureContent{
		StateRoot: msg.StateRoot,
		EscrowId:  fmt.Sprint(escrow.Id),
		Nonce:     msg.Nonce,
	}
	sigData, err := deterministicMarshal(sigContent)
	if err != nil {
		return fmt.Errorf("failed to marshal sig content: %w", err)
	}
	sigHash := sha256.Sum256(sigData)

	// Verify signatures and count slot votes
	seenSlots := make(map[uint32]bool, len(msg.Signatures))
	slotVotes := 0
	for _, sig := range msg.Signatures {
		if seenSlots[sig.SlotId] {
			return fmt.Errorf("duplicate signature for slot %d", sig.SlotId)
		}
		seenSlots[sig.SlotId] = true
		if int(sig.SlotId) >= len(escrow.Slots) {
			return fmt.Errorf("slot_id %d out of range", sig.SlotId)
		}
		expectedAddr := escrow.Slots[sig.SlotId]

		recovered, err := recoverCosmosAddress(sigHash[:], sig.Signature)
		if err != nil {
			return fmt.Errorf("failed to recover address for slot %d: %w", sig.SlotId, err)
		}
		if recovered.String() != expectedAddr {
			if isWarmKey == nil || !isWarmKey(expectedAddr, recovered.String()) {
				return fmt.Errorf("signature for slot %d recovered %s, expected %s", sig.SlotId, recovered.String(), expectedAddr)
			}
		}

		slotVotes++
	}

	// Check quorum: derived from actual slot count in escrow.
	requiredQuorum := DevshardQuorumFor(len(escrow.Slots))
	if slotVotes < requiredQuorum {
		return fmt.Errorf("insufficient quorum: %d slot votes, need %d", slotVotes, requiredQuorum)
	}

	// Verify total cost + fees does not exceed escrow amount
	slotCount := uint64(len(escrow.Slots))
	if slotCount == 0 {
		return fmt.Errorf("no slots in escrow")
	}
	seenStatSlots := make(map[uint32]bool, len(msg.HostStats))
	var totalCost uint64
	for _, hs := range msg.HostStats {
		if seenStatSlots[hs.SlotId] {
			return fmt.Errorf("duplicate host_stats slot_id %d", hs.SlotId)
		}
		seenStatSlots[hs.SlotId] = true
		assignedToSlot, err := devshardAssignedUpperBoundForSlot(msg.Nonce, slotCount, hs.SlotId)
		if err != nil {
			return err
		}
		if uint64(hs.Missed) > assignedToSlot {
			return fmt.Errorf("slot %d missed count %d exceeds assigned per slot %d", hs.SlotId, hs.Missed, assignedToSlot)
		}
		completed := assignedToSlot - uint64(hs.Missed)
		if uint64(hs.Invalid) > completed {
			return fmt.Errorf("slot %d invalid count %d exceeds completed per slot %d", hs.SlotId, hs.Invalid, completed)
		}
		nextTotalCost, carry := bits.Add64(totalCost, hs.Cost, 0)
		if carry != 0 {
			return fmt.Errorf("total cost overflow")
		}
		totalCost = nextTotalCost
	}
	totalDebit, carry := bits.Add64(totalCost, msg.Fees, 0)
	if carry != 0 {
		return fmt.Errorf("total cost plus fees overflow")
	}
	if totalDebit > escrow.Amount {
		return fmt.Errorf("total debit %d (cost %d + fees %d) exceeds escrow amount %d", totalDebit, totalCost, msg.Fees, escrow.Amount)
	}

	return nil
}

// deterministicMarshal uses gogoproto's XXX_Marshal with deterministic=true.
// This produces the same bytes as google.golang.org/protobuf's deterministic marshal
// for proto3 messages (fields serialized in field number order).
func deterministicMarshal(msg interface {
	XXX_Marshal(b []byte, deterministic bool) ([]byte, error)
}) ([]byte, error) {
	return msg.XXX_Marshal(nil, true)
}

// ComputeDevshardHostStatsHash recomputes the host stats hash from settlement host stats.
// Uses the same proto deterministic marshal as the devshard module.
func ComputeDevshardHostStatsHash(hostStats []*types.DevshardSettlementHostStats) ([]byte, error) {
	entries := make([]*types.DevshardHostStatsProto, len(hostStats))
	for i, hs := range hostStats {
		entries[i] = &types.DevshardHostStatsProto{
			SlotId:               hs.SlotId,
			Missed:               hs.Missed,
			Invalid:              hs.Invalid,
			Cost:                 hs.Cost,
			RequiredValidations:  hs.RequiredValidations,
			CompletedValidations: hs.CompletedValidations,
		}
	}
	slices.SortStableFunc(entries, func(a, b *types.DevshardHostStatsProto) int {
		return cmp.Compare(a.SlotId, b.SlotId)
	})
	mapProto := &types.DevshardHostStatsMapProto{Entries: entries}
	data, err := deterministicMarshal(mapProto)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	return hash[:], nil
}

// recoverCosmosAddress recovers a Cosmos bech32 address from a secp256k1 signature.
// The signature is in go-ethereum format: [R(32) || S(32) || V(1)].
// dcrd expects [V+27(1) || R(32) || S(32)].
func recoverCosmosAddress(hash []byte, sig []byte) (sdk.AccAddress, error) {
	if len(sig) != 65 {
		return nil, fmt.Errorf("signature must be 65 bytes, got %d", len(sig))
	}

	v := sig[64]
	dcrdSig := make([]byte, 65)
	dcrdSig[0] = v + 27
	copy(dcrdSig[1:33], sig[0:32])
	copy(dcrdSig[33:65], sig[32:64])

	pubKey, _, err := ecdsa.RecoverCompact(dcrdSig, hash)
	if err != nil {
		return nil, fmt.Errorf("ecrecover failed: %w", err)
	}

	cosmosPubKey := &cosmossecp.PubKey{Key: pubKey.SerializeCompressed()}
	return sdk.AccAddress(cosmosPubKey.Address()), nil
}

func (k Keeper) HasWarmKeyGrant(ctx context.Context, granter, grantee string) bool {
	resp, err := k.AuthzKeeper.Grants(ctx, &authztypes.QueryGrantsRequest{
		Granter:    granter,
		Grantee:    grantee,
		MsgTypeUrl: sdk.MsgTypeURL(&types.MsgStartInference{}),
	})
	return err == nil && resp != nil && len(resp.Grants) > 0
}
