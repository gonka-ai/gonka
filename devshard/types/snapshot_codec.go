package types

import (
	"fmt"
	"maps"

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
	maps.Copy(warmKeys, state.WarmKeys)

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
		Group:                         group,
		Balance:                       state.Balance,
		Fees:                          state.Fees,
		Phase:                         uint32(state.Phase),
		FinalizeNonce:                 state.FinalizeNonce,
		Inferences:                    inferences,
		HostStats:                     hostStats,
		WarmKeys:                      warmKeys,
		LatestNonce:                   state.LatestNonce,
		SealedAcc:                     append([]byte(nil), state.SealedAcc...),
		HeightSyncForcedStart:         state.HeightSyncForcedStart,
		HeightSyncForcedEnd:           state.HeightSyncForcedEnd,
		HeightSyncCadenceSwallowUntil: state.HeightSyncCadenceSwallowUntil,
		HeightSyncSwallowFe:           state.HeightSyncSwallowFe,
		HeightSyncTurnK:               state.HeightSyncTurnK,
		HeightSyncTurnSlots:           state.HeightSyncTurnSlots,
		HeightSyncTurnReason:          state.HeightSyncTurnReason,
	}
}

// EscrowStateFromProto maps protobuf wire form to domain EscrowState.
func EscrowStateFromProto(msg *EscrowStateProto) *EscrowState {
	if msg == nil {
		return nil
	}

	group := make([]SlotAssignment, len(msg.GetGroup()))
	for i, slot := range msg.GetGroup() {
		if slot == nil {
			continue
		}
		group[i] = SlotAssignment{
			SlotID:           slot.GetSlotId(),
			ValidatorAddress: slot.GetValidatorAddress(),
		}
	}

	inferences := make(map[uint64]*InferenceRecord, len(msg.GetInferences()))
	for id, rec := range msg.GetInferences() {
		inferences[id] = inferenceRecordFromProto(rec)
	}

	hostStats := make(map[uint32]*HostStats, len(msg.GetHostStats()))
	for slotID, stats := range msg.GetHostStats() {
		if stats == nil {
			continue
		}
		hostStats[slotID] = &HostStats{
			Missed:               stats.GetMissed(),
			Invalid:              stats.GetInvalid(),
			Cost:                 stats.GetCost(),
			RequiredValidations:  stats.GetRequiredValidations(),
			CompletedValidations: stats.GetCompletedValidations(),
		}
	}

	warmKeys := make(map[uint32]string, len(msg.GetWarmKeys()))
	maps.Copy(warmKeys, msg.GetWarmKeys())

	var cfg SessionConfig
	if msg.GetConfig() != nil {
		p := msg.GetConfig()
		cfg = SessionConfig{
			RefusalTimeout:            p.GetRefusalTimeout(),
			ExecutionTimeout:          p.GetExecutionTimeout(),
			TokenPrice:                p.GetTokenPrice(),
			CreateDevshardFee:         p.GetCreateDevshardFee(),
			FeePerNonce:               p.GetFeePerNonce(),
			VoteThreshold:             p.GetVoteThreshold(),
			ValidationRate:            p.GetValidationRate(),
			InferenceSealGraceNonces:  p.GetInferenceSealGraceNonces(),
			InferenceSealGraceSeconds: p.GetInferenceSealGraceSeconds(),
			AutoSealEveryNNonces:      p.GetAutoSealEveryNNonces(),
		}
	}

	return &EscrowState{
		EscrowID:                      msg.GetEscrowId(),
		StateRootAndProtocolVersion:   msg.GetStateRootAndProtocolVersion(),
		Config:                        cfg,
		Group:                         group,
		Balance:                       msg.GetBalance(),
		Fees:                          msg.GetFees(),
		Phase:                         SessionPhase(msg.GetPhase()),
		FinalizeNonce:                 msg.GetFinalizeNonce(),
		Inferences:                    inferences,
		HostStats:                     hostStats,
		WarmKeys:                      warmKeys,
		LatestNonce:                   msg.GetLatestNonce(),
		SealedAcc:                     append([]byte(nil), msg.GetSealedAcc()...),
		HeightSyncForcedStart:         msg.GetHeightSyncForcedStart(),
		HeightSyncForcedEnd:           msg.GetHeightSyncForcedEnd(),
		HeightSyncCadenceSwallowUntil: msg.GetHeightSyncCadenceSwallowUntil(),
		HeightSyncSwallowFe:           msg.GetHeightSyncSwallowFe(),
		HeightSyncTurnK:               msg.GetHeightSyncTurnK(),
		HeightSyncTurnSlots:           msg.GetHeightSyncTurnSlots(),
		HeightSyncTurnReason:          msg.GetHeightSyncTurnReason(),
	}
}

// MarshalStateSnapshotProto serializes a state snapshot envelope to protobuf.
// heightSyncFloor is derived RAM (not hashed); nil omits the field so legacy
// readers still load the hashed EscrowState.
func MarshalStateSnapshotProto(state *EscrowState, committedEntries map[uint64][]byte, sealedNonces map[uint64]uint64, heightSyncFloor *FloorIndexProto) ([]byte, error) {
	msg := &StateSnapshotProto{
		State:            EscrowStateToProto(state),
		CommittedEntries: cloneBytesMap(committedEntries),
		SealedNonces:     cloneUint64Map(sealedNonces),
		HeightSyncFloor:  heightSyncFloor,
	}
	return proto.Marshal(msg)
}

// UnmarshalStateSnapshotProto deserializes a protobuf state snapshot envelope.
// A nil height-sync floor means the snapshot predates the field (rebuild from the journal).
func UnmarshalStateSnapshotProto(data []byte) (*EscrowState, map[uint64][]byte, map[uint64]uint64, *FloorIndexProto, error) {
	msg := &StateSnapshotProto{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("unmarshal state snapshot proto: %w", err)
	}
	if msg.GetState() == nil {
		return nil, nil, nil, nil, fmt.Errorf("unmarshal state snapshot proto: missing state")
	}
	return EscrowStateFromProto(msg.GetState()), cloneBytesMap(msg.GetCommittedEntries()), cloneUint64Map(msg.GetSealedNonces()), msg.GetHeightSyncFloor(), nil
}

func inferenceRecordToProto(id uint64, rec *InferenceRecord) *InferenceRecordProto {
	if rec == nil {
		return &InferenceRecordProto{InferenceId: id}
	}
	return &InferenceRecordProto{
		InferenceId:       id,
		Status:            uint32(rec.Status),
		ExecutorSlot:      rec.ExecutorSlot,
		Model:             rec.Model,
		PromptHash:        append([]byte(nil), rec.PromptHash...),
		ResponseHash:      append([]byte(nil), rec.ResponseHash...),
		InputLength:       rec.InputLength,
		MaxTokens:         rec.MaxTokens,
		InputTokens:       rec.InputTokens,
		OutputTokens:      rec.OutputTokens,
		ReservedCost:      rec.ReservedCost,
		ActualCost:        rec.ActualCost,
		StartedAt:         rec.StartedAt,
		ConfirmedAt:       rec.ConfirmedAt,
		StartedAtHeight:   rec.StartedAtHeight,
		ConfirmedAtHeight: rec.ConfirmedAtHeight,
		VotesValid:        rec.VotesValid,
		VotesInvalid:      rec.VotesInvalid,
		ValidatedBy:       rec.ValidatedBy.Bytes(),
	}
}

func inferenceRecordFromProto(msg *InferenceRecordProto) *InferenceRecord {
	if msg == nil {
		return &InferenceRecord{}
	}
	return &InferenceRecord{
		Status:            InferenceStatus(msg.GetStatus()),
		ExecutorSlot:      msg.GetExecutorSlot(),
		Model:             msg.GetModel(),
		PromptHash:        append([]byte(nil), msg.GetPromptHash()...),
		ResponseHash:      append([]byte(nil), msg.GetResponseHash()...),
		InputLength:       msg.GetInputLength(),
		MaxTokens:         msg.GetMaxTokens(),
		InputTokens:       msg.GetInputTokens(),
		OutputTokens:      msg.GetOutputTokens(),
		ReservedCost:      msg.GetReservedCost(),
		ActualCost:        msg.GetActualCost(),
		StartedAt:         msg.GetStartedAt(),
		ConfirmedAt:       msg.GetConfirmedAt(),
		StartedAtHeight:   msg.GetStartedAtHeight(),
		ConfirmedAtHeight: msg.GetConfirmedAtHeight(),
		VotesValid:        msg.GetVotesValid(),
		VotesInvalid:      msg.GetVotesInvalid(),
		ValidatedBy:       Bitmap128FromBytes(msg.GetValidatedBy()),
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
	maps.Copy(out, src)
	return out
}
