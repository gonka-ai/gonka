package types

import "fmt"

// ContinuousPoCCommit represents a lightweight on-chain record of continuous PoC work.
// Participants submit these commits periodically while inference-serving to prove
// that unused GPU capacity is being utilized for PoC nonce generation.
type ContinuousPoCCommit struct {
	ParticipantAddress string `protobuf:"bytes,1,opt,name=participant_address,json=participantAddress,proto3" json:"participant_address,omitempty"`
	EpochIndex         uint64 `protobuf:"varint,2,opt,name=epoch_index,json=epochIndex,proto3" json:"epoch_index,omitempty"`
	NonceCount         uint32 `protobuf:"varint,3,opt,name=nonce_count,json=nonceCount,proto3" json:"nonce_count,omitempty"`
	RootHash           []byte `protobuf:"bytes,4,opt,name=root_hash,json=rootHash,proto3" json:"root_hash,omitempty"`
	InferenceCount     uint32 `protobuf:"varint,5,opt,name=inference_count,json=inferenceCount,proto3" json:"inference_count,omitempty"`
	CommitBlockHeight  int64  `protobuf:"varint,6,opt,name=commit_block_height,json=commitBlockHeight,proto3" json:"commit_block_height,omitempty"`
	GpuUtilizationBps  uint32 `protobuf:"varint,7,opt,name=gpu_utilization_bps,json=gpuUtilizationBps,proto3" json:"gpu_utilization_bps,omitempty"`
}

func (m *ContinuousPoCCommit) Reset()         { *m = ContinuousPoCCommit{} }
func (m *ContinuousPoCCommit) String() string { return "" }
func (m *ContinuousPoCCommit) ProtoMessage()  {}

// ContinuousPoCEpochSummary aggregates a participant's continuous PoC commits for an epoch.
// Updated on-chain as commits arrive; used during epoch settlement for weight calculation.
type ContinuousPoCEpochSummary struct {
	ParticipantAddress string `protobuf:"bytes,1,opt,name=participant_address,json=participantAddress,proto3" json:"participant_address,omitempty"`
	EpochIndex         uint64 `protobuf:"varint,2,opt,name=epoch_index,json=epochIndex,proto3" json:"epoch_index,omitempty"`
	TotalNonces        uint64 `protobuf:"varint,3,opt,name=total_nonces,json=totalNonces,proto3" json:"total_nonces,omitempty"`
	TotalInferences    uint64 `protobuf:"varint,4,opt,name=total_inferences,json=totalInferences,proto3" json:"total_inferences,omitempty"`
	CommitCount        uint32 `protobuf:"varint,5,opt,name=commit_count,json=commitCount,proto3" json:"commit_count,omitempty"`
	LastCommitHeight   int64  `protobuf:"varint,6,opt,name=last_commit_height,json=lastCommitHeight,proto3" json:"last_commit_height,omitempty"`
	EffectivePocWeight int64  `protobuf:"varint,7,opt,name=effective_poc_weight,json=effectivePocWeight,proto3" json:"effective_poc_weight,omitempty"`
}

func (m *ContinuousPoCEpochSummary) Reset()         { *m = ContinuousPoCEpochSummary{} }
func (m *ContinuousPoCEpochSummary) String() string { return "" }
func (m *ContinuousPoCEpochSummary) ProtoMessage()  {}

// MsgSubmitContinuousPoCCommit is the message type for submitting continuous PoC commits.
type MsgSubmitContinuousPoCCommit struct {
	Creator           string `protobuf:"bytes,1,opt,name=creator,proto3" json:"creator,omitempty"`
	EpochIndex        uint64 `protobuf:"varint,2,opt,name=epoch_index,json=epochIndex,proto3" json:"epoch_index,omitempty"`
	NonceCount        uint32 `protobuf:"varint,3,opt,name=nonce_count,json=nonceCount,proto3" json:"nonce_count,omitempty"`
	RootHash          []byte `protobuf:"bytes,4,opt,name=root_hash,json=rootHash,proto3" json:"root_hash,omitempty"`
	InferenceCount    uint32 `protobuf:"varint,5,opt,name=inference_count,json=inferenceCount,proto3" json:"inference_count,omitempty"`
	GpuUtilizationBps uint32 `protobuf:"varint,6,opt,name=gpu_utilization_bps,json=gpuUtilizationBps,proto3" json:"gpu_utilization_bps,omitempty"`
}

func (m *MsgSubmitContinuousPoCCommit) Reset()         { *m = MsgSubmitContinuousPoCCommit{} }
func (m *MsgSubmitContinuousPoCCommit) String() string { return "" }
func (m *MsgSubmitContinuousPoCCommit) ProtoMessage()  {}

// MsgSubmitContinuousPoCCommitResponse is the response type for MsgSubmitContinuousPoCCommit.
type MsgSubmitContinuousPoCCommitResponse struct{}

func (m *MsgSubmitContinuousPoCCommitResponse) Reset()         { *m = MsgSubmitContinuousPoCCommitResponse{} }
func (m *MsgSubmitContinuousPoCCommitResponse) String() string { return "" }
func (m *MsgSubmitContinuousPoCCommitResponse) ProtoMessage()  {}

// ContinuousPoCParams defines parameters for the continuous PoC feature.
// When enabled, GPU nodes generate PoC nonces in parallel with inference,
// utilizing spare capacity to prove computational readiness.
type ContinuousPoCParams struct {
	// EnableContinuousPoC activates the continuous PoC system.
	EnableContinuousPoC bool `protobuf:"varint,1,opt,name=enable_continuous_poc,json=enableContinuousPoc,proto3" json:"enable_continuous_poc,omitempty"`
	// PocUtilizationTargetBps is the fraction of idle GPU capacity for PoC, in basis points [0, 10000].
	PocUtilizationTargetBps uint32 `protobuf:"varint,2,opt,name=poc_utilization_target_bps,json=pocUtilizationTargetBps,proto3" json:"poc_utilization_target_bps,omitempty"`
	// NonceWeight is how many PoC nonces equate to one inference unit of work.
	NonceWeight uint32 `protobuf:"varint,3,opt,name=nonce_weight,json=nonceWeight,proto3" json:"nonce_weight,omitempty"`
	// MaxCommitsPerEpoch prevents spam by limiting commits per epoch per participant.
	MaxCommitsPerEpoch uint32 `protobuf:"varint,4,opt,name=max_commits_per_epoch,json=maxCommitsPerEpoch,proto3" json:"max_commits_per_epoch,omitempty"`
	// MinNoncesPerCommit is the minimum nonce count required per commit.
	MinNoncesPerCommit uint32 `protobuf:"varint,5,opt,name=min_nonces_per_commit,json=minNoncesPerCommit,proto3" json:"min_nonces_per_commit,omitempty"`
	// ValidationSampleRateBps is the probability [0, 10000] that a commit gets validated.
	ValidationSampleRateBps uint32 `protobuf:"varint,6,opt,name=validation_sample_rate_bps,json=validationSampleRateBps,proto3" json:"validation_sample_rate_bps,omitempty"`
}

func (m *ContinuousPoCParams) Reset()         { *m = ContinuousPoCParams{} }
func (m *ContinuousPoCParams) String() string { return "" }
func (m *ContinuousPoCParams) ProtoMessage()  {}

// Equal implements the gogoproto Equal interface for ContinuousPoCParams.
func (this *ContinuousPoCParams) Equal(that interface{}) bool {
	if that == nil {
		return this == nil
	}
	that1, ok := that.(*ContinuousPoCParams)
	if !ok {
		that2, ok := that.(ContinuousPoCParams)
		if ok {
			that1 = &that2
		} else {
			return false
		}
	}
	if that1 == nil {
		return this == nil
	} else if this == nil {
		return false
	}
	if this.EnableContinuousPoC != that1.EnableContinuousPoC {
		return false
	}
	if this.PocUtilizationTargetBps != that1.PocUtilizationTargetBps {
		return false
	}
	if this.NonceWeight != that1.NonceWeight {
		return false
	}
	if this.MaxCommitsPerEpoch != that1.MaxCommitsPerEpoch {
		return false
	}
	if this.MinNoncesPerCommit != that1.MinNoncesPerCommit {
		return false
	}
	if this.ValidationSampleRateBps != that1.ValidationSampleRateBps {
		return false
	}
	return true
}

// Validate checks that the continuous PoC params are reasonable.
func (p *ContinuousPoCParams) Validate() error {
	if p == nil {
		return nil
	}
	if p.PocUtilizationTargetBps > 10000 {
		return fmt.Errorf("poc_utilization_target_bps must be <= 10000, got %d", p.PocUtilizationTargetBps)
	}
	if p.ValidationSampleRateBps > 10000 {
		return fmt.Errorf("validation_sample_rate_bps must be <= 10000, got %d", p.ValidationSampleRateBps)
	}
	if p.NonceWeight == 0 && p.EnableContinuousPoC {
		return fmt.Errorf("nonce_weight cannot be 0 when continuous PoC is enabled")
	}
	return nil
}
