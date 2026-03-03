package types

import (
	"crypto/sha256"
	"fmt"
	io "io"
	math_bits "math/bits"
)

// ── Commit ────────────────────────────────────────────────────────────────────

// ContinuousPoCCommit is a lightweight on-chain record of continuous PoC work.
// Participants submit these periodically while serving inference to prove that
// idle GPU capacity is being used for nonce generation in parallel.
type ContinuousPoCCommit struct {
	ParticipantAddress string `protobuf:"bytes,1,opt,name=participant_address,json=participantAddress,proto3" json:"participant_address,omitempty"`
	EpochIndex         uint64 `protobuf:"varint,2,opt,name=epoch_index,json=epochIndex,proto3" json:"epoch_index,omitempty"`
	NonceCount         uint32 `protobuf:"varint,3,opt,name=nonce_count,json=nonceCount,proto3" json:"nonce_count,omitempty"`
	// RootHash is the Merkle root over sha256(nonce_i) leaves (32 bytes).
	RootHash          []byte `protobuf:"bytes,4,opt,name=root_hash,json=rootHash,proto3" json:"root_hash,omitempty"`
	InferenceCount    uint32 `protobuf:"varint,5,opt,name=inference_count,json=inferenceCount,proto3" json:"inference_count,omitempty"`
	CommitBlockHeight int64  `protobuf:"varint,6,opt,name=commit_block_height,json=commitBlockHeight,proto3" json:"commit_block_height,omitempty"`
	GpuUtilizationBps uint32 `protobuf:"varint,7,opt,name=gpu_utilization_bps,json=gpuUtilizationBps,proto3" json:"gpu_utilization_bps,omitempty"`
}

func (m *ContinuousPoCCommit) Reset()         { *m = ContinuousPoCCommit{} }
func (m *ContinuousPoCCommit) String() string { return fmt.Sprintf("%+v", *m) }
func (m *ContinuousPoCCommit) ProtoMessage()  {}

func (m *ContinuousPoCCommit) Marshal() (dAtA []byte, err error) {
	size := m.Size()
	dAtA = make([]byte, size)
	n, err := m.MarshalToSizedBuffer(dAtA[:size])
	if err != nil {
		return nil, err
	}
	return dAtA[:n], nil
}

func (m *ContinuousPoCCommit) MarshalTo(dAtA []byte) (int, error) {
	size := m.Size()
	return m.MarshalToSizedBuffer(dAtA[:size])
}

func (m *ContinuousPoCCommit) MarshalToSizedBuffer(dAtA []byte) (int, error) {
	i := len(dAtA)
	_ = i
	if m.GpuUtilizationBps != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.GpuUtilizationBps))
		i--
		dAtA[i] = 0x38
	}
	if m.CommitBlockHeight != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.CommitBlockHeight))
		i--
		dAtA[i] = 0x30
	}
	if m.InferenceCount != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.InferenceCount))
		i--
		dAtA[i] = 0x28
	}
	if len(m.RootHash) > 0 {
		i -= len(m.RootHash)
		copy(dAtA[i:], m.RootHash)
		i = encodeVarintCPoC(dAtA, i, uint64(len(m.RootHash)))
		i--
		dAtA[i] = 0x22
	}
	if m.NonceCount != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.NonceCount))
		i--
		dAtA[i] = 0x18
	}
	if m.EpochIndex != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.EpochIndex))
		i--
		dAtA[i] = 0x10
	}
	if len(m.ParticipantAddress) > 0 {
		i -= len(m.ParticipantAddress)
		copy(dAtA[i:], m.ParticipantAddress)
		i = encodeVarintCPoC(dAtA, i, uint64(len(m.ParticipantAddress)))
		i--
		dAtA[i] = 0xa
	}
	return len(dAtA) - i, nil
}

func (m *ContinuousPoCCommit) Size() (n int) {
	if m == nil {
		return 0
	}
	var l int
	_ = l
	l = len(m.ParticipantAddress)
	if l > 0 {
		n += 1 + l + sovCPoC(uint64(l))
	}
	if m.EpochIndex != 0 {
		n += 1 + sovCPoC(uint64(m.EpochIndex))
	}
	if m.NonceCount != 0 {
		n += 1 + sovCPoC(uint64(m.NonceCount))
	}
	l = len(m.RootHash)
	if l > 0 {
		n += 1 + l + sovCPoC(uint64(l))
	}
	if m.InferenceCount != 0 {
		n += 1 + sovCPoC(uint64(m.InferenceCount))
	}
	if m.CommitBlockHeight != 0 {
		n += 1 + sovCPoC(uint64(m.CommitBlockHeight))
	}
	if m.GpuUtilizationBps != 0 {
		n += 1 + sovCPoC(uint64(m.GpuUtilizationBps))
	}
	return n
}

func (m *ContinuousPoCCommit) Unmarshal(dAtA []byte) error {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		preIndex := iNdEx
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return fmt.Errorf("proto: integer overflow")
			}
			if iNdEx >= l {
				return io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= uint64(b&0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		fieldNum := int32(wire >> 3)
		wireType := int(wire & 0x7)
		switch fieldNum {
		case 1:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field ParticipantAddress", wireType)
			}
			var slen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				slen |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if slen < 0 {
				return fmt.Errorf("proto: negative length")
			}
			postIndex := iNdEx + slen
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.ParticipantAddress = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		case 2:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field EpochIndex", wireType)
			}
			m.EpochIndex = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.EpochIndex |= uint64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 3:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field NonceCount", wireType)
			}
			m.NonceCount = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.NonceCount |= uint32(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 4:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field RootHash", wireType)
			}
			var blen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				blen |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if blen < 0 {
				return fmt.Errorf("proto: negative length")
			}
			postIndex := iNdEx + blen
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.RootHash = make([]byte, blen)
			copy(m.RootHash, dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		case 5:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field InferenceCount", wireType)
			}
			m.InferenceCount = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.InferenceCount |= uint32(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 6:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field CommitBlockHeight", wireType)
			}
			m.CommitBlockHeight = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.CommitBlockHeight |= int64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 7:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field GpuUtilizationBps", wireType)
			}
			m.GpuUtilizationBps = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.GpuUtilizationBps |= uint32(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		default:
			iNdEx = preIndex
			skippy, err := skipCPoC(dAtA[iNdEx:])
			if err != nil {
				return err
			}
			iNdEx += skippy
		}
		if iNdEx < 0 {
			return fmt.Errorf("proto: negative iNdEx")
		}
	}
	return nil
}

// ── EpochSummary ──────────────────────────────────────────────────────────────

// ContinuousPoCEpochSummary aggregates a participant's continuous PoC commits for an epoch.
// Updated on-chain as commits arrive; used during epoch settlement for weight calculation.
type ContinuousPoCEpochSummary struct {
	ParticipantAddress string `protobuf:"bytes,1,opt,name=participant_address,json=participantAddress,proto3" json:"participant_address,omitempty"`
	EpochIndex         uint64 `protobuf:"varint,2,opt,name=epoch_index,json=epochIndex,proto3" json:"epoch_index,omitempty"`
	TotalNonces        uint64 `protobuf:"varint,3,opt,name=total_nonces,json=totalNonces,proto3" json:"total_nonces,omitempty"`
	TotalInferences    uint64 `protobuf:"varint,4,opt,name=total_inferences,json=totalInferences,proto3" json:"total_inferences,omitempty"`
	CommitCount        uint32 `protobuf:"varint,5,opt,name=commit_count,json=commitCount,proto3" json:"commit_count,omitempty"`
	LastCommitHeight   int64  `protobuf:"varint,6,opt,name=last_commit_height,json=lastCommitHeight,proto3" json:"last_commit_height,omitempty"`
	// EffectivePocWeight = total_nonces / nonce_weight; added to standard PoC weight at epoch settlement.
	EffectivePocWeight int64 `protobuf:"varint,7,opt,name=effective_poc_weight,json=effectivePocWeight,proto3" json:"effective_poc_weight,omitempty"`
	// PenaltyApplied is true when a challenge failed or expired for this epoch, zeroing the weight contribution.
	PenaltyApplied bool `protobuf:"varint,8,opt,name=penalty_applied,json=penaltyApplied,proto3" json:"penalty_applied,omitempty"`
}

func (m *ContinuousPoCEpochSummary) Reset()         { *m = ContinuousPoCEpochSummary{} }
func (m *ContinuousPoCEpochSummary) String() string { return fmt.Sprintf("%+v", *m) }
func (m *ContinuousPoCEpochSummary) ProtoMessage()  {}

func (m *ContinuousPoCEpochSummary) Marshal() (dAtA []byte, err error) {
	size := m.Size()
	dAtA = make([]byte, size)
	n, err := m.MarshalToSizedBuffer(dAtA[:size])
	if err != nil {
		return nil, err
	}
	return dAtA[:n], nil
}

func (m *ContinuousPoCEpochSummary) MarshalTo(dAtA []byte) (int, error) {
	size := m.Size()
	return m.MarshalToSizedBuffer(dAtA[:size])
}

func (m *ContinuousPoCEpochSummary) MarshalToSizedBuffer(dAtA []byte) (int, error) {
	i := len(dAtA)
	_ = i
	if m.PenaltyApplied {
		i--
		if m.PenaltyApplied {
			dAtA[i] = 1
		} else {
			dAtA[i] = 0
		}
		i--
		dAtA[i] = 0x40
	}
	if m.EffectivePocWeight != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.EffectivePocWeight))
		i--
		dAtA[i] = 0x38
	}
	if m.LastCommitHeight != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.LastCommitHeight))
		i--
		dAtA[i] = 0x30
	}
	if m.CommitCount != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.CommitCount))
		i--
		dAtA[i] = 0x28
	}
	if m.TotalInferences != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.TotalInferences))
		i--
		dAtA[i] = 0x20
	}
	if m.TotalNonces != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.TotalNonces))
		i--
		dAtA[i] = 0x18
	}
	if m.EpochIndex != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.EpochIndex))
		i--
		dAtA[i] = 0x10
	}
	if len(m.ParticipantAddress) > 0 {
		i -= len(m.ParticipantAddress)
		copy(dAtA[i:], m.ParticipantAddress)
		i = encodeVarintCPoC(dAtA, i, uint64(len(m.ParticipantAddress)))
		i--
		dAtA[i] = 0xa
	}
	return len(dAtA) - i, nil
}

func (m *ContinuousPoCEpochSummary) Size() (n int) {
	if m == nil {
		return 0
	}
	var l int
	_ = l
	l = len(m.ParticipantAddress)
	if l > 0 {
		n += 1 + l + sovCPoC(uint64(l))
	}
	if m.EpochIndex != 0 {
		n += 1 + sovCPoC(uint64(m.EpochIndex))
	}
	if m.TotalNonces != 0 {
		n += 1 + sovCPoC(uint64(m.TotalNonces))
	}
	if m.TotalInferences != 0 {
		n += 1 + sovCPoC(uint64(m.TotalInferences))
	}
	if m.CommitCount != 0 {
		n += 1 + sovCPoC(uint64(m.CommitCount))
	}
	if m.LastCommitHeight != 0 {
		n += 1 + sovCPoC(uint64(m.LastCommitHeight))
	}
	if m.EffectivePocWeight != 0 {
		n += 1 + sovCPoC(uint64(m.EffectivePocWeight))
	}
	if m.PenaltyApplied {
		n += 2
	}
	return n
}

func (m *ContinuousPoCEpochSummary) Unmarshal(dAtA []byte) error {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		preIndex := iNdEx
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return fmt.Errorf("proto: integer overflow")
			}
			if iNdEx >= l {
				return io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= uint64(b&0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		fieldNum := int32(wire >> 3)
		wireType := int(wire & 0x7)
		switch fieldNum {
		case 1:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field ParticipantAddress", wireType)
			}
			var slen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				slen |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if slen < 0 {
				return fmt.Errorf("proto: negative length")
			}
			postIndex := iNdEx + slen
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.ParticipantAddress = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		case 2:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field EpochIndex", wireType)
			}
			m.EpochIndex = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.EpochIndex |= uint64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 3:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field TotalNonces", wireType)
			}
			m.TotalNonces = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.TotalNonces |= uint64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 4:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field TotalInferences", wireType)
			}
			m.TotalInferences = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.TotalInferences |= uint64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 5:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field CommitCount", wireType)
			}
			m.CommitCount = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.CommitCount |= uint32(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 6:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field LastCommitHeight", wireType)
			}
			m.LastCommitHeight = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.LastCommitHeight |= int64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 7:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field EffectivePocWeight", wireType)
			}
			m.EffectivePocWeight = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.EffectivePocWeight |= int64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 8:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field PenaltyApplied", wireType)
			}
			var v int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				v |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			m.PenaltyApplied = bool(v != 0)
		default:
			iNdEx = preIndex
			skippy, err := skipCPoC(dAtA[iNdEx:])
			if err != nil {
				return err
			}
			iNdEx += skippy
		}
		if iNdEx < 0 {
			return fmt.Errorf("proto: negative iNdEx")
		}
	}
	return nil
}

// ── Params ────────────────────────────────────────────────────────────────────

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
	// ValidationSampleRateBps is the probability [0, 10000] that a commit is challenged in EndBlock.
	ValidationSampleRateBps uint32 `protobuf:"varint,6,opt,name=validation_sample_rate_bps,json=validationSampleRateBps,proto3" json:"validation_sample_rate_bps,omitempty"`
	// ValidationChallengeDeadlineBlocks is the number of blocks the participant has to respond to a challenge.
	ValidationChallengeDeadlineBlocks int64 `protobuf:"varint,7,opt,name=validation_challenge_deadline_blocks,json=validationChallengeDeadlineBlocks,proto3" json:"validation_challenge_deadline_blocks,omitempty"`
}

func (m *ContinuousPoCParams) Reset()         { *m = ContinuousPoCParams{} }
func (m *ContinuousPoCParams) String() string { return fmt.Sprintf("%+v", *m) }
func (m *ContinuousPoCParams) ProtoMessage()  {}

func (m *ContinuousPoCParams) Marshal() (dAtA []byte, err error) {
	size := m.Size()
	dAtA = make([]byte, size)
	n, err := m.MarshalToSizedBuffer(dAtA[:size])
	if err != nil {
		return nil, err
	}
	return dAtA[:n], nil
}

func (m *ContinuousPoCParams) MarshalTo(dAtA []byte) (int, error) {
	size := m.Size()
	return m.MarshalToSizedBuffer(dAtA[:size])
}

func (m *ContinuousPoCParams) MarshalToSizedBuffer(dAtA []byte) (int, error) {
	i := len(dAtA)
	_ = i
	if m.ValidationChallengeDeadlineBlocks != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.ValidationChallengeDeadlineBlocks))
		i--
		dAtA[i] = 0x38
	}
	if m.ValidationSampleRateBps != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.ValidationSampleRateBps))
		i--
		dAtA[i] = 0x30
	}
	if m.MinNoncesPerCommit != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.MinNoncesPerCommit))
		i--
		dAtA[i] = 0x28
	}
	if m.MaxCommitsPerEpoch != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.MaxCommitsPerEpoch))
		i--
		dAtA[i] = 0x20
	}
	if m.NonceWeight != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.NonceWeight))
		i--
		dAtA[i] = 0x18
	}
	if m.PocUtilizationTargetBps != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.PocUtilizationTargetBps))
		i--
		dAtA[i] = 0x10
	}
	if m.EnableContinuousPoC {
		i--
		if m.EnableContinuousPoC {
			dAtA[i] = 1
		} else {
			dAtA[i] = 0
		}
		i--
		dAtA[i] = 0x8
	}
	return len(dAtA) - i, nil
}

func (m *ContinuousPoCParams) Size() (n int) {
	if m == nil {
		return 0
	}
	var l int
	_ = l
	if m.EnableContinuousPoC {
		n += 2
	}
	if m.PocUtilizationTargetBps != 0 {
		n += 1 + sovCPoC(uint64(m.PocUtilizationTargetBps))
	}
	if m.NonceWeight != 0 {
		n += 1 + sovCPoC(uint64(m.NonceWeight))
	}
	if m.MaxCommitsPerEpoch != 0 {
		n += 1 + sovCPoC(uint64(m.MaxCommitsPerEpoch))
	}
	if m.MinNoncesPerCommit != 0 {
		n += 1 + sovCPoC(uint64(m.MinNoncesPerCommit))
	}
	if m.ValidationSampleRateBps != 0 {
		n += 1 + sovCPoC(uint64(m.ValidationSampleRateBps))
	}
	if m.ValidationChallengeDeadlineBlocks != 0 {
		n += 1 + sovCPoC(uint64(m.ValidationChallengeDeadlineBlocks))
	}
	return n
}

func (m *ContinuousPoCParams) Unmarshal(dAtA []byte) error {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		preIndex := iNdEx
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return fmt.Errorf("proto: integer overflow")
			}
			if iNdEx >= l {
				return io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= uint64(b&0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		fieldNum := int32(wire >> 3)
		wireType := int(wire & 0x7)
		switch fieldNum {
		case 1:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field EnableContinuousPoC", wireType)
			}
			var v int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				v |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			m.EnableContinuousPoC = bool(v != 0)
		case 2:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field PocUtilizationTargetBps", wireType)
			}
			m.PocUtilizationTargetBps = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.PocUtilizationTargetBps |= uint32(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 3:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field NonceWeight", wireType)
			}
			m.NonceWeight = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.NonceWeight |= uint32(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 4:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field MaxCommitsPerEpoch", wireType)
			}
			m.MaxCommitsPerEpoch = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.MaxCommitsPerEpoch |= uint32(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 5:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field MinNoncesPerCommit", wireType)
			}
			m.MinNoncesPerCommit = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.MinNoncesPerCommit |= uint32(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 6:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field ValidationSampleRateBps", wireType)
			}
			m.ValidationSampleRateBps = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.ValidationSampleRateBps |= uint32(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 7:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field ValidationChallengeDeadlineBlocks", wireType)
			}
			m.ValidationChallengeDeadlineBlocks = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.ValidationChallengeDeadlineBlocks |= int64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		default:
			iNdEx = preIndex
			skippy, err := skipCPoC(dAtA[iNdEx:])
			if err != nil {
				return err
			}
			iNdEx += skippy
		}
		if iNdEx < 0 {
			return fmt.Errorf("proto: negative iNdEx")
		}
	}
	return nil
}

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
	return this.EnableContinuousPoC == that1.EnableContinuousPoC &&
		this.PocUtilizationTargetBps == that1.PocUtilizationTargetBps &&
		this.NonceWeight == that1.NonceWeight &&
		this.MaxCommitsPerEpoch == that1.MaxCommitsPerEpoch &&
		this.MinNoncesPerCommit == that1.MinNoncesPerCommit &&
		this.ValidationSampleRateBps == that1.ValidationSampleRateBps &&
		this.ValidationChallengeDeadlineBlocks == that1.ValidationChallengeDeadlineBlocks
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
	if p.EnableContinuousPoC && p.ValidationChallengeDeadlineBlocks <= 0 {
		return fmt.Errorf("validation_challenge_deadline_blocks must be > 0 when continuous PoC is enabled")
	}
	return nil
}

// ── Challenge / Response messages ────────────────────────────────────────────

// ContinuousPoCChallenge records a pending Merkle proof challenge issued by the chain.
// The challenge asks the participant to reveal nonce at position NonceIndex within the
// commit's Merkle tree, proving the nonce was actually computed.
type ContinuousPoCChallenge struct {
	ChallengedAddress   string `protobuf:"bytes,1,opt,name=challenged_address,json=challengedAddress,proto3" json:"challenged_address,omitempty"`
	EpochIndex          uint64 `protobuf:"varint,2,opt,name=epoch_index,json=epochIndex,proto3" json:"epoch_index,omitempty"`
	CommitBlockHeight   int64  `protobuf:"varint,3,opt,name=commit_block_height,json=commitBlockHeight,proto3" json:"commit_block_height,omitempty"`
	NonceIndex          uint32 `protobuf:"varint,4,opt,name=nonce_index,json=nonceIndex,proto3" json:"nonce_index,omitempty"`
	DeadlineBlockHeight int64  `protobuf:"varint,5,opt,name=deadline_block_height,json=deadlineBlockHeight,proto3" json:"deadline_block_height,omitempty"`
	ChallengeBlockHeight int64 `protobuf:"varint,6,opt,name=challenge_block_height,json=challengeBlockHeight,proto3" json:"challenge_block_height,omitempty"`
	Resolved            bool   `protobuf:"varint,7,opt,name=resolved,proto3" json:"resolved,omitempty"`
}

func (m *ContinuousPoCChallenge) Reset()         { *m = ContinuousPoCChallenge{} }
func (m *ContinuousPoCChallenge) String() string { return fmt.Sprintf("%+v", *m) }
func (m *ContinuousPoCChallenge) ProtoMessage()  {}

func (m *ContinuousPoCChallenge) Marshal() (dAtA []byte, err error) {
	size := m.Size()
	dAtA = make([]byte, size)
	n, err := m.MarshalToSizedBuffer(dAtA[:size])
	if err != nil {
		return nil, err
	}
	return dAtA[:n], nil
}

func (m *ContinuousPoCChallenge) MarshalTo(dAtA []byte) (int, error) {
	size := m.Size()
	return m.MarshalToSizedBuffer(dAtA[:size])
}

func (m *ContinuousPoCChallenge) MarshalToSizedBuffer(dAtA []byte) (int, error) {
	i := len(dAtA)
	_ = i
	if m.Resolved {
		i--
		if m.Resolved {
			dAtA[i] = 1
		} else {
			dAtA[i] = 0
		}
		i--
		dAtA[i] = 0x38
	}
	if m.ChallengeBlockHeight != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.ChallengeBlockHeight))
		i--
		dAtA[i] = 0x30
	}
	if m.DeadlineBlockHeight != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.DeadlineBlockHeight))
		i--
		dAtA[i] = 0x28
	}
	if m.NonceIndex != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.NonceIndex))
		i--
		dAtA[i] = 0x20
	}
	if m.CommitBlockHeight != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.CommitBlockHeight))
		i--
		dAtA[i] = 0x18
	}
	if m.EpochIndex != 0 {
		i = encodeVarintCPoC(dAtA, i, uint64(m.EpochIndex))
		i--
		dAtA[i] = 0x10
	}
	if len(m.ChallengedAddress) > 0 {
		i -= len(m.ChallengedAddress)
		copy(dAtA[i:], m.ChallengedAddress)
		i = encodeVarintCPoC(dAtA, i, uint64(len(m.ChallengedAddress)))
		i--
		dAtA[i] = 0xa
	}
	return len(dAtA) - i, nil
}

func (m *ContinuousPoCChallenge) Size() (n int) {
	if m == nil {
		return 0
	}
	var l int
	_ = l
	l = len(m.ChallengedAddress)
	if l > 0 {
		n += 1 + l + sovCPoC(uint64(l))
	}
	if m.EpochIndex != 0 {
		n += 1 + sovCPoC(uint64(m.EpochIndex))
	}
	if m.CommitBlockHeight != 0 {
		n += 1 + sovCPoC(uint64(m.CommitBlockHeight))
	}
	if m.NonceIndex != 0 {
		n += 1 + sovCPoC(uint64(m.NonceIndex))
	}
	if m.DeadlineBlockHeight != 0 {
		n += 1 + sovCPoC(uint64(m.DeadlineBlockHeight))
	}
	if m.ChallengeBlockHeight != 0 {
		n += 1 + sovCPoC(uint64(m.ChallengeBlockHeight))
	}
	if m.Resolved {
		n += 2
	}
	return n
}

func (m *ContinuousPoCChallenge) Unmarshal(dAtA []byte) error {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		preIndex := iNdEx
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return fmt.Errorf("proto: integer overflow")
			}
			if iNdEx >= l {
				return io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= uint64(b&0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		fieldNum := int32(wire >> 3)
		wireType := int(wire & 0x7)
		switch fieldNum {
		case 1:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field ChallengedAddress", wireType)
			}
			var slen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				slen |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if slen < 0 {
				return fmt.Errorf("proto: negative length")
			}
			postIndex := iNdEx + slen
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.ChallengedAddress = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		case 2:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field EpochIndex", wireType)
			}
			m.EpochIndex = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.EpochIndex |= uint64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 3:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field CommitBlockHeight", wireType)
			}
			m.CommitBlockHeight = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.CommitBlockHeight |= int64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 4:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field NonceIndex", wireType)
			}
			m.NonceIndex = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.NonceIndex |= uint32(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 5:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field DeadlineBlockHeight", wireType)
			}
			m.DeadlineBlockHeight = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.DeadlineBlockHeight |= int64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 6:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field ChallengeBlockHeight", wireType)
			}
			m.ChallengeBlockHeight = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.ChallengeBlockHeight |= int64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 7:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field Resolved", wireType)
			}
			var v int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				v |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			m.Resolved = bool(v != 0)
		default:
			iNdEx = preIndex
			skippy, err := skipCPoC(dAtA[iNdEx:])
			if err != nil {
				return err
			}
			iNdEx += skippy
		}
		if iNdEx < 0 {
			return fmt.Errorf("proto: negative iNdEx")
		}
	}
	return nil
}

// ── Tx message types ──────────────────────────────────────────────────────────

// MsgSubmitContinuousPoCCommit submits a continuous PoC commit.
type MsgSubmitContinuousPoCCommit struct {
	Creator           string `protobuf:"bytes,1,opt,name=creator,proto3" json:"creator,omitempty"`
	EpochIndex        uint64 `protobuf:"varint,2,opt,name=epoch_index,json=epochIndex,proto3" json:"epoch_index,omitempty"`
	NonceCount        uint32 `protobuf:"varint,3,opt,name=nonce_count,json=nonceCount,proto3" json:"nonce_count,omitempty"`
	RootHash          []byte `protobuf:"bytes,4,opt,name=root_hash,json=rootHash,proto3" json:"root_hash,omitempty"`
	InferenceCount    uint32 `protobuf:"varint,5,opt,name=inference_count,json=inferenceCount,proto3" json:"inference_count,omitempty"`
	GpuUtilizationBps uint32 `protobuf:"varint,6,opt,name=gpu_utilization_bps,json=gpuUtilizationBps,proto3" json:"gpu_utilization_bps,omitempty"`
}

func (m *MsgSubmitContinuousPoCCommit) Reset()         { *m = MsgSubmitContinuousPoCCommit{} }
func (m *MsgSubmitContinuousPoCCommit) String() string { return fmt.Sprintf("%+v", *m) }
func (m *MsgSubmitContinuousPoCCommit) ProtoMessage()  {}

// MsgSubmitContinuousPoCCommitResponse is the response for MsgSubmitContinuousPoCCommit.
type MsgSubmitContinuousPoCCommitResponse struct{}

func (m *MsgSubmitContinuousPoCCommitResponse) Reset()         {}
func (m *MsgSubmitContinuousPoCCommitResponse) String() string { return "" }
func (m *MsgSubmitContinuousPoCCommitResponse) ProtoMessage()  {}

// MsgRespondContinuousPoCChallenge provides the Merkle proof for a pending challenge.
// The participant reveals the nonce preimage at the challenged index and the Merkle path.
type MsgRespondContinuousPoCChallenge struct {
	Creator              string   `protobuf:"bytes,1,opt,name=creator,proto3" json:"creator,omitempty"`
	EpochIndex           uint64   `protobuf:"varint,2,opt,name=epoch_index,json=epochIndex,proto3" json:"epoch_index,omitempty"`
	ChallengeBlockHeight int64    `protobuf:"varint,3,opt,name=challenge_block_height,json=challengeBlockHeight,proto3" json:"challenge_block_height,omitempty"`
	// LeafValue is the raw nonce preimage; sha256(LeafValue) must be the challenged leaf.
	LeafValue []byte `protobuf:"bytes,4,opt,name=leaf_value,json=leafValue,proto3" json:"leaf_value,omitempty"`
	// ProofSiblings are the sibling hashes along the Merkle path (leaf → root).
	ProofSiblings [][]byte `protobuf:"bytes,5,rep,name=proof_siblings,json=proofSiblings,proto3" json:"proof_siblings,omitempty"`
	// ProofDirs indicates whether each sibling is on the left (false) or right (true).
	ProofDirs []bool `protobuf:"varint,6,rep,packed,name=proof_dirs,json=proofDirs,proto3" json:"proof_dirs,omitempty"`
}

func (m *MsgRespondContinuousPoCChallenge) Reset()         { *m = MsgRespondContinuousPoCChallenge{} }
func (m *MsgRespondContinuousPoCChallenge) String() string { return fmt.Sprintf("%+v", *m) }
func (m *MsgRespondContinuousPoCChallenge) ProtoMessage()  {}

// MsgRespondContinuousPoCChallengeResponse is the response for MsgRespondContinuousPoCChallenge.
type MsgRespondContinuousPoCChallengeResponse struct{}

func (m *MsgRespondContinuousPoCChallengeResponse) Reset()         {}
func (m *MsgRespondContinuousPoCChallengeResponse) String() string { return "" }
func (m *MsgRespondContinuousPoCChallengeResponse) ProtoMessage()  {}

// ── Merkle verification ───────────────────────────────────────────────────────

// VerifyMerkleProof verifies a Merkle proof for a leaf at the given index.
//
// The tree uses standard binary Merkle construction:
//   - leaf hash:     sha256(leaf_value)
//   - internal node: sha256(left_child || right_child)
//
// leafIndex identifies the position of the leaf (0-based) in the original leaf array.
// rootHash is the expected Merkle root stored on-chain in the commit.
func VerifyMerkleProof(leafValue []byte, leafIndex uint32, siblings [][]byte, dirs []bool, rootHash []byte) bool {
	if len(siblings) != len(dirs) {
		return false
	}
	current := sha256.Sum256(leafValue)
	for i, sibling := range siblings {
		if len(sibling) != 32 {
			return false
		}
		var combined []byte
		if dirs[i] {
			// sibling is on the left
			combined = append(sibling, current[:]...)
		} else {
			// sibling is on the right
			combined = append(current[:], sibling...)
		}
		h := sha256.Sum256(combined)
		current = h
	}
	if len(rootHash) != 32 {
		return false
	}
	for i := range current {
		if current[i] != rootHash[i] {
			return false
		}
	}
	return true
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func encodeVarintCPoC(dAtA []byte, offset int, v uint64) int {
	offset -= sovCPoC(v)
	base := offset
	for v >= 1<<7 {
		dAtA[offset] = uint8(v&0x7f | 0x80)
		v >>= 7
		offset++
	}
	dAtA[offset] = uint8(v)
	return base
}

func sovCPoC(x uint64) (n int) {
	return (math_bits.Len64(x|1) + 6) / 7
}

func skipCPoC(dAtA []byte) (n int, err error) {
	l := len(dAtA)
	iNdEx := 0
	depth := 0
	for iNdEx < l {
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return 0, fmt.Errorf("proto: integer overflow")
			}
			if iNdEx >= l {
				return 0, io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= (uint64(b) & 0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		wireType := int(wire & 0x7)
		switch wireType {
		case 0:
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return 0, fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return 0, io.ErrUnexpectedEOF
				}
				iNdEx++
				if dAtA[iNdEx-1] < 0x80 {
					break
				}
			}
		case 1:
			iNdEx += 8
		case 2:
			var length int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return 0, fmt.Errorf("proto: integer overflow")
				}
				if iNdEx >= l {
					return 0, io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				length |= (int(b) & 0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if length < 0 {
				return 0, fmt.Errorf("proto: negative length")
			}
			iNdEx += length
		case 3:
			depth++
		case 4:
			if depth == 0 {
				return 0, fmt.Errorf("proto: unexpected end of group")
			}
			depth--
		case 5:
			iNdEx += 4
		default:
			return 0, fmt.Errorf("proto: illegal wireType %d", wireType)
		}
		if iNdEx < 0 {
			return 0, fmt.Errorf("proto: negative iNdEx")
		}
		if depth == 0 {
			return iNdEx, nil
		}
	}
	return 0, io.ErrUnexpectedEOF
}
