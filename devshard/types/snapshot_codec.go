package types

import (
	"fmt"

	"google.golang.org/protobuf/proto"
)

// EscrowStateToProto maps domain EscrowState to its protobuf wire form.
func EscrowStateToProto(state *EscrowState) *EscrowStateProto {
	if state == nil {
		return nil
	}

	group := make([]*SlotAssignmentProto, len(state.Group))
	for i := range state.Group {
		group[i] = &SlotAssignmentProto{
			SlotId:           state.Group[i].SlotID,
			ValidatorAddress: state.Group[i].ValidatorAddress,
		}
	}

	inferences := make(map[uint64]*InferenceRecordProto, len(state.Inferences))
	for id, rec := range state.Inferences {
		inferences[id] = inferenceRecordToProto(id, rec)
	}

	hostStats := make(map[uint32]*HostStatsProto, len(state.HostStats))
	for slotID, stats := range state.HostStats {
		hostStats[slotID] = &HostStatsProto{
			SlotId:               slotID,
			Missed:               stats.Missed,
			Invalid:              stats.Invalid,
			Cost:                 stats.Cost,
			RequiredValidations:  stats.RequiredValidations,
			CompletedValidations: stats.CompletedValidations,
		}
	}

	warmKeys := make(map[uint32]string, len(state.WarmKeys))
	for slotID, addr := range state.WarmKeys {
		warmKeys[slotID] = addr
	}

	cfg := state.Config
	return &EscrowStateProto{
		EscrowId:                    state.EscrowID,
		StateRootAndProtocolVersion: state.StateRootAndProtocolVersion,
		Config: &SessionConfigProto{
			RefusalTimeout:            cfg.RefusalTimeout,
			ExecutionTimeout:          cfg.ExecutionTimeout,
			TokenPrice:                cfg.TokenPrice,
			CreateDevshardFee:         cfg.CreateDevshardFee,
			FeePerNonce:               cfg.FeePerNonce,
			VoteThreshold:             cfg.VoteThreshold,
			ValidationRate:            cfg.ValidationRate,
			InferenceSealGraceNonces:  cfg.InferenceSealGraceNonces,
			InferenceSealGraceSeconds: cfg.InferenceSealGraceSeconds,
			AutoSealEveryNNonces:      cfg.AutoSealEveryNNonces,
		},
		Group:         group,
		Balance:       state.Balance,
		Fees:          state.Fees,
		Phase:         uint32(state.Phase),
		FinalizeNonce: state.FinalizeNonce,
		Inferences:    inferences,
		HostStats:     hostStats,
		WarmKeys:      warmKeys,
		LatestNonce:   state.LatestNonce,
		SealedAcc:     append([]byte(nil), state.SealedAcc...),
	}
}

// EscrowStateFromProto maps protobuf wire form to domain EscrowState.
func EscrowStateFromProto(msg *EscrowStateProto) *EscrowState {
	if msg == nil {
		return nil
	}

	group := make([]SlotAssignment, len(msg.Group))
	for i, slot := range msg.Group {
		if slot == nil {
			continue
		}
		group[i] = SlotAssignment{
			SlotID:           slot.SlotId,
			ValidatorAddress: slot.ValidatorAddress,
		}
	}

	inferences := make(map[uint64]*InferenceRecord, len(msg.Inferences))
	for id, rec := range msg.Inferences {
		inferences[id] = inferenceRecordFromProto(rec)
	}

	hostStats := make(map[uint32]*HostStats, len(msg.HostStats))
	for slotID, stats := range msg.HostStats {
		if stats == nil {
			continue
		}
		hostStats[slotID] = &HostStats{
			Missed:               stats.Missed,
			Invalid:              stats.Invalid,
			Cost:                 stats.Cost,
			RequiredValidations:  stats.RequiredValidations,
			CompletedValidations: stats.CompletedValidations,
		}
	}

	warmKeys := make(map[uint32]string, len(msg.WarmKeys))
	for slotID, addr := range msg.WarmKeys {
		warmKeys[slotID] = addr
	}

	var cfg SessionConfig
	if msg.Config != nil {
		p := msg.Config
		cfg = SessionConfig{
			RefusalTimeout:            p.RefusalTimeout,
			ExecutionTimeout:          p.ExecutionTimeout,
			TokenPrice:                p.TokenPrice,
			CreateDevshardFee:         p.CreateDevshardFee,
			FeePerNonce:               p.FeePerNonce,
			VoteThreshold:             p.VoteThreshold,
			ValidationRate:            p.ValidationRate,
			InferenceSealGraceNonces:  p.InferenceSealGraceNonces,
			InferenceSealGraceSeconds: p.InferenceSealGraceSeconds,
			AutoSealEveryNNonces:      p.AutoSealEveryNNonces,
		}
	}

	return &EscrowState{
		EscrowID:                    msg.EscrowId,
		StateRootAndProtocolVersion: msg.StateRootAndProtocolVersion,
		Config:                      cfg,
		Group:                       group,
		Balance:                     msg.Balance,
		Fees:                        msg.Fees,
		Phase:                       SessionPhase(msg.Phase),
		FinalizeNonce:               msg.FinalizeNonce,
		Inferences:                  inferences,
		HostStats:                   hostStats,
		WarmKeys:                    warmKeys,
		LatestNonce:                 msg.LatestNonce,
		SealedAcc:                   append([]byte(nil), msg.SealedAcc...),
	}
}

// MarshalStateSnapshotProto serializes a state snapshot envelope to protobuf.
func MarshalStateSnapshotProto(state *EscrowState, committedEntries map[uint64][]byte, sealedNonces map[uint64]uint64) ([]byte, error) {
	msg := &StateSnapshotProto{
		State:            EscrowStateToProto(state),
		CommittedEntries: cloneBytesMap(committedEntries),
		SealedNonces:     cloneUint64Map(sealedNonces),
	}
	return proto.Marshal(msg)
}

// UnmarshalStateSnapshotProto deserializes a protobuf state snapshot envelope.
func UnmarshalStateSnapshotProto(data []byte) (*EscrowState, map[uint64][]byte, map[uint64]uint64, error) {
	msg := &StateSnapshotProto{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return nil, nil, nil, fmt.Errorf("unmarshal state snapshot proto: %w", err)
	}
	if msg.State == nil {
		return nil, nil, nil, fmt.Errorf("unmarshal state snapshot proto: missing state")
	}
	return EscrowStateFromProto(msg.State), cloneBytesMap(msg.CommittedEntries), cloneUint64Map(msg.SealedNonces), nil
}

func inferenceRecordToProto(id uint64, rec *InferenceRecord) *InferenceRecordProto {
	if rec == nil {
		return &InferenceRecordProto{InferenceId: id}
	}
	return &InferenceRecordProto{
		InferenceId:  id,
		Status:       uint32(rec.Status),
		ExecutorSlot: rec.ExecutorSlot,
		Model:        rec.Model,
		PromptHash:   append([]byte(nil), rec.PromptHash...),
		ResponseHash: append([]byte(nil), rec.ResponseHash...),
		InputLength:  rec.InputLength,
		MaxTokens:    rec.MaxTokens,
		InputTokens:  rec.InputTokens,
		OutputTokens: rec.OutputTokens,
		ReservedCost: rec.ReservedCost,
		ActualCost:   rec.ActualCost,
		StartedAt:    rec.StartedAt,
		ConfirmedAt:  rec.ConfirmedAt,
		VotesValid:   rec.VotesValid,
		VotesInvalid: rec.VotesInvalid,
		ValidatedBy:  rec.ValidatedBy.Bytes(),
	}
}

func inferenceRecordFromProto(msg *InferenceRecordProto) *InferenceRecord {
	if msg == nil {
		return &InferenceRecord{}
	}
	return &InferenceRecord{
		Status:       InferenceStatus(msg.Status),
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
		ValidatedBy:  Bitmap128FromBytes(msg.ValidatedBy),
	}
}

func cloneBytesMap(src map[uint64][]byte) map[uint64][]byte {
	if len(src) == 0 {
		return nil
	}
	out := make(map[uint64][]byte, len(src))
	for k, v := range src {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

func cloneUint64Map(src map[uint64]uint64) map[uint64]uint64 {
	if len(src) == 0 {
		return nil
	}
	out := make(map[uint64]uint64, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
