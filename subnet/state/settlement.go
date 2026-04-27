package state

import (
	"crypto/sha256"
	"fmt"

	"subnet/signing"
	"subnet/types"
)

// SettlementPayload contains the data needed for on-chain settlement.
// Mainnet recomputes the state root from HostStats + RestHash + phase byte;
// it is not included in the payload.
type SettlementPayload struct {
	EscrowID   string
	Nonce      uint64
	RestHash   []byte
	HostStats  map[uint32]*types.HostStats
	Signatures map[uint32][]byte
}

// BuildSettlement constructs a SettlementPayload from the final escrow state.
func BuildSettlement(escrowID string, st types.EscrowState, signatures map[uint32][]byte, nonce uint64) (*SettlementPayload, error) {
	restHash, err := ComputeRestHash(st.Balance, st.Inferences, st.WarmKeys)
	if err != nil {
		return nil, err
	}

	return &SettlementPayload{
		EscrowID:   escrowID,
		Nonce:      nonce,
		RestHash:   restHash,
		HostStats:  st.HostStats,
		Signatures: signatures,
	}, nil
}

// VerifySettlement recomputes the state root from the payload, verifies host
// signatures over it, and checks that the signing quorum meets 2/3+1 of the
// group size. Returns the verified state root on success.
func VerifySettlement(
	payload SettlementPayload,
	group []types.SlotAssignment,
	verifier signing.Verifier,
	warmKeys map[uint32]string,
) ([]byte, error) {
	if len(group) == 0 {
		return nil, fmt.Errorf("empty group")
	}

	// 1. Recompute state root: sha256(host_stats_hash || rest_hash || 0x02).
	hostStatsHash, err := ComputeHostStatsHash(payload.HostStats)
	if err != nil {
		return nil, fmt.Errorf("compute host stats hash: %w", err)
	}

	h := sha256.New()
	h.Write(hostStatsHash)
	h.Write(payload.RestHash)
	h.Write([]byte{uint8(types.PhaseSettlement)})
	stateRoot := h.Sum(nil)

	// 2. Build the signed message: proto(StateSignatureContent{state_root, escrow_id, nonce}).
	sigContent := &types.StateSignatureContent{
		StateRoot: stateRoot,
		EscrowId:  payload.EscrowID,
		Nonce:     payload.Nonce,
	}
	sigData, err := deterministicMarshal.Marshal(sigContent)
	if err != nil {
		return nil, fmt.Errorf("marshal signature content: %w", err)
	}

	// Build slot_id -> cold address and cold address -> total slot count.
	slotToAddr := make(map[uint32]string, len(group))
	addressSlots := make(map[string]uint32, len(group))
	for _, sa := range group {
		slotToAddr[sa.SlotID] = sa.ValidatorAddress
		addressSlots[sa.ValidatorAddress]++
	}

	// 3. Verify each signature and accumulate weight.
	// One signature per cold address counts for all slots owned by that address.
	verified := make(map[string]bool, len(payload.Signatures))
	totalWeight := uint32(0)

	for slotID, sig := range payload.Signatures {
		addr, err := verifier.RecoverAddress(sigData, sig)
		if err != nil {
			return nil, fmt.Errorf("recover address: %w", err)
		}

		coldAddr, ok := slotToAddr[slotID]
		if !ok {
			return nil, fmt.Errorf("slot %d not in group", slotID)
		}

		// Accept if recovered address matches cold key or warm key for this slot.
		if addr != coldAddr {
			if warmKeys == nil || warmKeys[slotID] != addr {
				return nil, fmt.Errorf("signer %s not in group", addr)
			}
		}

		// Track by cold address for multi-slot dedup.
		if verified[coldAddr] {
			continue
		}
		verified[coldAddr] = true
		totalWeight += addressSlots[coldAddr]
	}

	// 4. Quorum check: total weight >= 2*len(group)/3 + 1.
	required := uint32(2*len(group)/3 + 1)
	if totalWeight < required {
		return nil, fmt.Errorf("insufficient quorum: got %d, need %d", totalWeight, required)
	}

	return stateRoot, nil
}
