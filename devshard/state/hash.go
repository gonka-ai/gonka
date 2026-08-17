package state

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"

	"google.golang.org/protobuf/encoding/protowire"
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
//	version_hash    = sha256(protocol version tag)       -- 32 bytes
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
	version string,
) ([]byte, error) {
	hostStatsHash, err := computeHostStatsHash(hostStats)
	if err != nil {
		return nil, err
	}
	acc := sealedAccBytes32(nil)
	restHash, err := ComputeRestHashV2(balance, acc, inferences, warmKeys)
	if err != nil {
		return nil, err
	}

	return ComputeStateRootFromRestHash(hostStatsHash, restHash, fees, phase, version), nil
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

// ComputeInferencesHashV2 returns sha256(sealed_acc || live_inferences_hash)
// where live_inferences_hash is the same encoding as v1's inference-set hash
// over the live map only (sorted by inference id).
func ComputeInferencesHashV2(sealedAcc [32]byte, liveInferences map[uint64]*types.InferenceRecord) ([]byte, error) {
	liveHash, err := computeInferencesHash(liveInferences)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write(sealedAcc[:])
	h.Write(liveHash)
	return h.Sum(nil), nil
}

// ComputeRestHashV2 returns sha256(balance_be || inferences_hash_v2 || warm_keys_hash)
// for Phase 1 v2 sessions (sealed accumulator + live inference set).
func ComputeRestHashV2(balance uint64, sealedAcc [32]byte, liveInferences map[uint64]*types.InferenceRecord, warmKeys map[uint32]string) ([]byte, error) {
	infHash, err := ComputeInferencesHashV2(sealedAcc, liveInferences)
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

func sealedAccBytes32(b []byte) [32]byte {
	var z [32]byte
	if len(b) >= 32 {
		copy(z[:], b[:32])
	} else {
		copy(z[:], b)
	}
	return z
}

// FoldSealedAccumulator updates the v2 sealed accumulator:
//
//	out = sha256(acc || seal_nonce_be || inf_id_be || committed_entry)
//
// committed_entry must be the canonical protobuf bytes for that inference at
// seal time (same bytes that contributed to the v1 inference hash).
func FoldSealedAccumulator(acc [32]byte, sealNonce, infID uint64, committedEntry []byte) [32]byte {
	h := sha256.New()
	h.Write(acc[:])
	var u64be [8]byte
	binary.BigEndian.PutUint64(u64be[:], sealNonce)
	h.Write(u64be[:])
	binary.BigEndian.PutUint64(u64be[:], infID)
	h.Write(u64be[:])
	h.Write(committedEntry)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// ComputeStateRootFromRestHash computes the canonical state root when host
// stats hash and rest hash are already available.
func ComputeStateRootFromRestHash(hostStatsHash []byte, restHash []byte, fees uint64, phase types.SessionPhase, version string) []byte {
	// Encode fees as fixed-width big-endian to preserve deterministic hashing.
	feesBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(feesBytes, fees)
	versionHash := ComputeVersionHash(version)

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
	sum := sha256.Sum256([]byte(version))
	return sum[:]
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
	entries := make(map[uint64][]byte, len(inferences))
	for id, rec := range inferences {
		entry, err := marshalInferenceEntry(id, rec)
		if err != nil {
			return nil, err
		}
		entries[id] = entry
	}
	return computeInferencesHashFromEntries(entries), nil
}

func marshalInferenceEntry(id uint64, r *types.InferenceRecord) ([]byte, error) {
	data, err := deterministicMarshal.Marshal(&types.InferenceRecordProto{
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
	if err != nil {
		return nil, fmt.Errorf("marshal inference %d: %w", id, err)
	}
	return data, nil
}

func unmarshalInferenceEntry(data []byte) (uint64, *types.InferenceRecord, error) {
	msg := &types.InferenceRecordProto{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return 0, nil, fmt.Errorf("unmarshal inference entry: %w", err)
	}
	rec := &types.InferenceRecord{
		Status:       types.InferenceStatus(msg.Status),
		ExecutorSlot: msg.ExecutorSlot,
		Model:        msg.Model,
		PromptHash:   append([]byte(nil), msg.PromptHash...),
		ResponseHash: append([]byte(nil), msg.ResponseHash...),
		InputLength:  msg.InputLength,
		MaxTokens:    msg.MaxTokens,
		InputTokens:  msg.InputTokens,
		OutputTokens: msg.OutputTokens,
		ReservedCost: msg.ReservedCost,
		ActualCost:   msg.ActualCost,
		StartedAt:    msg.StartedAt,
		ConfirmedAt:  msg.ConfirmedAt,
		VotesValid:   msg.VotesValid,
		VotesInvalid: msg.VotesInvalid,
		ValidatedBy:  types.Bitmap128FromBytes(msg.ValidatedBy),
	}
	return msg.InferenceId, rec, nil
}

func computeInferencesHashFromEntries(entries map[uint64][]byte) []byte {
	ids := make([]uint64, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b uint64) int { return cmp.Compare(a, b) })

	buf := make([]byte, 0, len(entries)*64)
	for _, id := range ids {
		entry := entries[id]
		buf = protowire.AppendTag(buf, 1, protowire.BytesType)
		buf = protowire.AppendVarint(buf, uint64(len(entry)))
		buf = append(buf, entry...)
	}

	sum := sha256.Sum256(buf)
	return sum[:]
}
