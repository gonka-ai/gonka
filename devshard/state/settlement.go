package state

import (
	"crypto/sha256"
	"fmt"

	"devshard/signing"
	"devshard/types"
)

// SettlementPayload contains the data needed for on-chain settlement.
// Mainnet recomputes the state root from the payload. Explicit v0.2.11
// sessions use HostStats + RestHash + phase byte; v1+ sessions use
// HostStats + Fees + RestHash + VersionHash + phase byte.
// The state root itself is not included in the payload.
type SettlementPayload struct {
	EscrowID        string
	Version         string
	ProtocolVersion types.ProtocolVersion
	Nonce           uint64
	// Fees is the cumulative amount deducted from escrow balance as protocol fees.
	Fees       uint64
	RestHash   []byte
	HostStats  map[uint32]*types.HostStats
	Signatures map[uint32][]byte
}

// BuildSettlement constructs a SettlementPayload from the final escrow state.
func BuildSettlement(escrowID string, st types.EscrowState, signatures map[uint32][]byte, nonce uint64) (*SettlementPayload, error) {
	return BuildSettlementForProtocol(escrowID, st, signatures, nonce, "")
}

// BuildSettlementForProtocol constructs a SettlementPayload using the runtime
// protocol version configured for this escrow. Version remains the session /
// route version used by the current settlement preimage; ProtocolVersion is
// the compatibility switch for legacy runtime semantics.
func BuildSettlementForProtocol(escrowID string, st types.EscrowState, signatures map[uint32][]byte, nonce uint64, protocol types.ProtocolVersion) (*SettlementPayload, error) {
	restHash, err := ComputeRestHash(st.Balance, st.Inferences, st.WarmKeys)
	if err != nil {
		return nil, err
	}

	return &SettlementPayload{
		EscrowID:        escrowID,
		Version:         types.NormalizeSessionVersion(st.Version),
		ProtocolVersion: protocol,
		Nonce:           nonce,
		Fees:            st.Fees,
		RestHash:        restHash,
		HostStats:       st.HostStats,
		Signatures:      signatures,
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

	// 1. Recompute state root using deterministic settlement root preimage.
	if payload.Version == "" {
		return nil, fmt.Errorf("empty version")
	}
	hostStatsHash, err := ComputeHostStatsHash(payload.HostStats)
	if err != nil {
		return nil, fmt.Errorf("compute host stats hash: %w", err)
	}
	stateRoot := ComputeSettlementStateRootForProtocol(payload.ProtocolVersion, hostStatsHash, payload.RestHash, payload.Fees, types.PhaseSettlement, payload.Version)

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

// ComputeSettlementStateRoot computes the settlement root for either explicit
// v0.2.11 sessions or the normal v1+ version-bound settlement preimage.
func ComputeSettlementStateRoot(hostStatsHash []byte, restHash []byte, fees uint64, phase types.SessionPhase, version string) []byte {
	if IsSettlementVersionV0211(version) {
		return ComputeSettlementStateRootV0211(hostStatsHash, restHash, phase)
	}
	return ComputeStateRootFromRestHash(hostStatsHash, restHash, fees, phase, version)
}

// ComputeSettlementStateRootForProtocol computes the settlement root using the
// runtime protocol configured for the escrow. This deliberately does not infer
// compatibility from the session/route Version field.
func ComputeSettlementStateRootForProtocol(protocol types.ProtocolVersion, hostStatsHash []byte, restHash []byte, fees uint64, phase types.SessionPhase, version string) []byte {
	if protocol == types.ProtocolV0211 {
		return ComputeSettlementStateRootV0211(hostStatsHash, restHash, phase)
	}
	return ComputeStateRootFromRestHash(hostStatsHash, restHash, fees, phase, version)
}

// ComputeSettlementStateRootV0211 computes the explicit v0.2.11 settlement
// root preimage: host stats hash, rest hash, and phase byte.
func ComputeSettlementStateRootV0211(hostStatsHash []byte, restHash []byte, phase types.SessionPhase) []byte {
	h := sha256.New()
	h.Write(hostStatsHash)
	h.Write(restHash)
	h.Write([]byte{uint8(phase)})
	return h.Sum(nil)
}

// IsSettlementVersionV0211 reports whether a settlement payload explicitly
// requested the v0.2.11 settlement root preimage. Empty and "v1" payload
// versions continue through the normal version-bound path.
func IsSettlementVersionV0211(version string) bool {
	return version == string(types.ProtocolV0211)
}
