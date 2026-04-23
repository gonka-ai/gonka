package keeper

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"fmt"
	"slices"

	cosmossecp "github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/productscience/inference/x/inference/types"
)

const (
	SubnetGroupSize      = 16
	SubnetQuorumSlots    = 2*SubnetGroupSize/3 + 1
	// SubnetSettlementPhase is the phase byte appended to the state root preimage.
	// The chain hardcodes 0x02 (Settlement) so only fully-finalized subnet states
	// can pass verification. States at phase Active (0x00) or Finalizing (0x01)
	// produce a different hash and are rejected by the state_root mismatch check.
	SubnetSettlementPhase = byte(0x02)
)

// SubnetQuorumFor returns the minimum slot votes required for a given group size.
func SubnetQuorumFor(groupSize int) int {
	return 2*groupSize/3 + 1
}

// WarmKeyChecker returns true if grantee has an authz grant from granter.
type WarmKeyChecker func(granter, grantee string) bool

// VerifySubnetSettlement verifies settlement proof: state root, signatures, quorum, cost.
// If isWarmKey is non-nil, mismatched signatures are checked against authz grants.
func VerifySubnetSettlement(escrow types.SubnetEscrow, msg *types.MsgSettleSubnetEscrow, isWarmKey WarmKeyChecker) error {
	if escrow.Settled {
		return fmt.Errorf("escrow %d already settled", escrow.Id)
	}
	if msg.Settler != escrow.Creator {
		return fmt.Errorf("settler %s is not the escrow creator %s", msg.Settler, escrow.Creator)
	}

	// Recompute host_stats_hash
	hostStatsHash, err := ComputeSubnetHostStatsHash(msg.HostStats)
	if err != nil {
		return fmt.Errorf("failed to compute host stats hash: %w", err)
	}

	// Verify state_root = sha256(host_stats_hash || rest_hash || 0x02)
	rootInput := make([]byte, 0, len(hostStatsHash)+len(msg.RestHash)+1)
	rootInput = append(rootInput, hostStatsHash...)
	rootInput = append(rootInput, msg.RestHash...)
	rootInput = append(rootInput, SubnetSettlementPhase)
	expectedRoot := sha256.Sum256(rootInput)
	if len(msg.StateRoot) != 32 {
		return fmt.Errorf("state_root must be 32 bytes, got %d", len(msg.StateRoot))
	}
	if !bytes.Equal(expectedRoot[:], msg.StateRoot) {
		return fmt.Errorf("state_root mismatch")
	}

	// Build signature data using deterministic proto marshal
	sigContent := &types.SubnetStateSignatureContent{
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
	requiredQuorum := SubnetQuorumFor(len(escrow.Slots))
	if slotVotes < requiredQuorum {
		return fmt.Errorf("insufficient quorum: %d slot votes, need %d", slotVotes, requiredQuorum)
	}

	// Verify total cost does not exceed escrow amount
	seenStatSlots := make(map[uint32]bool, len(msg.HostStats))
	var totalCost uint64
	for _, hs := range msg.HostStats {
		if seenStatSlots[hs.SlotId] {
			return fmt.Errorf("duplicate host_stats slot_id %d", hs.SlotId)
		}
		seenStatSlots[hs.SlotId] = true
		totalCost += hs.Cost
	}
	if totalCost > escrow.Amount {
		return fmt.Errorf("total cost %d exceeds escrow amount %d", totalCost, escrow.Amount)
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

// ComputeSubnetHostStatsHash recomputes the host stats hash from settlement host stats.
// Uses the same proto deterministic marshal as the subnet module.
func ComputeSubnetHostStatsHash(hostStats []*types.SubnetSettlementHostStats) ([]byte, error) {
	entries := make([]*types.SubnetHostStatsProto, len(hostStats))
	for i, hs := range hostStats {
		entries[i] = &types.SubnetHostStatsProto{
			SlotId:               hs.SlotId,
			Missed:               hs.Missed,
			Invalid:              hs.Invalid,
			Cost:                 hs.Cost,
			RequiredValidations:  hs.RequiredValidations,
			CompletedValidations: hs.CompletedValidations,
		}
	}
	slices.SortFunc(entries, func(a, b *types.SubnetHostStatsProto) int {
		return cmp.Compare(a.SlotId, b.SlotId)
	})
	mapProto := &types.SubnetHostStatsMapProto{Entries: entries}
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
