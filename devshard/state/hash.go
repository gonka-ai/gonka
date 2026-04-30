package state

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"

	"google.golang.org/protobuf/proto"

	"devshard/types"
)

var deterministicMarshal = proto.MarshalOptions{Deterministic: true}

// ComputeStateRoot computes a flat commitment hash over the session state:
//
//	version_hash = sha256(version_utf8)
//	state_root   = sha256(host_stats_hash || fees_be || rest_hash || version_hash || phase_byte)
//
// where:
//
//	host_stats_hash = sha256(proto(sorted host stats))    -- 32 bytes
//	rest_hash       = sha256(balance_be || inferences_hash || warm_keys_hash) -- 32 bytes
//	fees_be         = uint64 fees in big-endian            -- 8 bytes
//	version_hash    = sha256(bound session version)        -- 32 bytes
//	warm_keys_hash  = sha256(sorted slot_id_be || addr_bytes)
//	inferences_hash = sha256(proto(sorted inference records))
//	phase_byte      = uint8(phase): 0x00=Active, 0x01=Finalizing, 0x02=Settlement
//
// All components have fixed, known lengths (32 + 8 + 32 + 32 + 1), so the
// concatenation is unambiguous without length prefixes.
//
// Mainnet settlement hardcodes phase_byte=0x02 when recomputing, rejecting
// any pre-settlement state.
func ComputeStateRoot(
	balance uint64,
	hostStats map[uint32]*types.HostStats,
	inferences map[uint64]*types.InferenceRecord,
	phase types.SessionPhase,
	warmKeys map[uint32]string,
	fees uint64,
	version ...string,
) ([]byte, error) {
	hostStatsHash, err := computeHostStatsHash(hostStats)
	if err != nil {
		return nil, err
	}
	restHash, err := computeRestHash(balance, inferences, warmKeys)
	if err != nil {
		return nil, err
	}

	return ComputeStateRootFromRestHash(hostStatsHash, restHash, fees, phase, version...), nil
}

// ComputeHostStatsHash computes sha256(proto(sorted host stats)).
// Exported for settlement verification on mainnet.
func ComputeHostStatsHash(hostStats map[uint32]*types.HostStats) ([]byte, error) {
	return computeHostStatsHash(hostStats)
}

// ComputeRestHash computes sha256(balance_be || inferences_hash || warm_keys_hash).
// Exported for settlement verification on mainnet.
func ComputeRestHash(balance uint64, inferences map[uint64]*types.InferenceRecord, warmKeys map[uint32]string) ([]byte, error) {
	return computeRestHash(balance, inferences, warmKeys)
}

// ComputeStateRootFromRestHash computes the canonical state root when host
// stats hash and rest hash are already available.
func ComputeStateRootFromRestHash(hostStatsHash []byte, restHash []byte, fees uint64, phase types.SessionPhase, version ...string) []byte {
	// Encode fees as fixed-width big-endian to preserve deterministic hashing.
	feesBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(feesBytes, fees)
	versionHash := ComputeVersionHash(resolveVersion(version...))

	h := sha256.New()
	h.Write(hostStatsHash)
	h.Write(feesBytes)
	h.Write(restHash)
	h.Write(versionHash)
	h.Write([]byte{uint8(phase)})
	return h.Sum(nil)
}

// ComputeVersionHash computes sha256 over the bound session version string.
func ComputeVersionHash(version string) []byte {
	sum := sha256.Sum256([]byte(types.NormalizeSessionVersion(version)))
	return sum[:]
}

func resolveVersion(version ...string) string {
	if len(version) == 0 {
		return types.LegacySessionVersion
	}
	return types.NormalizeSessionVersion(version[0])
}

// computeWarmKeysHash computes sha256 over sorted (slotID, address) pairs.
// Deterministic: entries sorted by slot ID, each serialized as 4-byte BE slot
// ID followed by UTF-8 address bytes with a 4-byte BE length prefix.
func computeWarmKeysHash(warmKeys map[uint32]string) []byte {
	if len(warmKeys) == 0 {
		empty := sha256.Sum256(nil)
		return empty[:]
	}

	slotIDs := make([]uint32, 0, len(warmKeys))
	for id := range warmKeys {
		slotIDs = append(slotIDs, id)
	}
	slices.SortFunc(slotIDs, func(a, b uint32) int { return cmp.Compare(a, b) })

	h := sha256.New()
	buf := make([]byte, 4)
	for _, id := range slotIDs {
		binary.BigEndian.PutUint32(buf, id)
		h.Write(buf)
		addr := []byte(warmKeys[id])
		binary.BigEndian.PutUint32(buf, uint32(len(addr)))
		h.Write(buf)
		h.Write(addr)
	}
	sum := h.Sum(nil)
	return sum
}

func computeHostStatsHash(hostStats map[uint32]*types.HostStats) ([]byte, error) {
	// Sort slot IDs for determinism.
	slotIDs := make([]uint32, 0, len(hostStats))
	for id := range hostStats {
		slotIDs = append(slotIDs, id)
	}
	slices.SortFunc(slotIDs, func(a, b uint32) int { return cmp.Compare(a, b) })

	entries := make([]*types.HostStatsProto, 0, len(slotIDs))
	for _, id := range slotIDs {
		s := hostStats[id]
		entries = append(entries, &types.HostStatsProto{
			SlotId:               id,
			Missed:               s.Missed,
			Invalid:              s.Invalid,
			Cost:                 s.Cost,
			RequiredValidations:  s.RequiredValidations,
			CompletedValidations: s.CompletedValidations,
		})
	}

	msg := &types.HostStatsMapProto{Entries: entries}
	data, err := deterministicMarshal.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal host stats: %w", err)
	}
	hash := sha256.Sum256(data)
	return hash[:], nil
}

func computeRestHash(balance uint64, inferences map[uint64]*types.InferenceRecord, warmKeys map[uint32]string) ([]byte, error) {
	infHash, err := computeInferencesHash(inferences)
	if err != nil {
		return nil, err
	}
	warmKeysHash := computeWarmKeysHash(warmKeys)

	balBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(balBytes, balance)

	h := sha256.New()
	h.Write(balBytes)
	h.Write(infHash)
	h.Write(warmKeysHash)
	return h.Sum(nil), nil
}

func computeInferencesHash(inferences map[uint64]*types.InferenceRecord) ([]byte, error) {
	// Sort inference IDs for determinism.
	ids := make([]uint64, 0, len(inferences))
	for id := range inferences {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b uint64) int { return cmp.Compare(a, b) })

	entries := make([]*types.InferenceRecordProto, 0, len(ids))
	for _, id := range ids {
		r := inferences[id]

		entries = append(entries, &types.InferenceRecordProto{
			InferenceId:  id,
			Status:       uint32(r.Status),
			ExecutorSlot: r.ExecutorSlot,
			Model:        r.Model,
			PromptHash:   r.PromptHash,
			ResponseHash: r.ResponseHash,
			InputLength:  r.InputLength,
			MaxTokens:    r.MaxTokens,
			InputTokens:  r.InputTokens,
			OutputTokens: r.OutputTokens,
			ReservedCost: r.ReservedCost,
			ActualCost:   r.ActualCost,
			StartedAt:    r.StartedAt,
			ConfirmedAt:  r.ConfirmedAt,
			VotesValid:   r.VotesValid,
			VotesInvalid: r.VotesInvalid,
			ValidatedBy:  r.ValidatedBy.Bytes(),
		})
	}

	msg := &types.InferencesMapProto{Entries: entries}
	data, err := deterministicMarshal.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal inferences: %w", err)
	}
	hash := sha256.Sum256(data)
	return hash[:], nil
}
