package types

import (
	"fmt"
	io "io"
	math_bits "math/bits"
)

// ── CacheQualityParams ─────────────────────────────────────────────────────────
//
// CacheQualityParams controls the semantic cache quality trust mechanism.
// When enabled, participants that operate as semantic cache providers earn an
// additional weight bonus proportional to how many times the protocol reused
// their BLS-signed inference results — creating a positive feedback loop:
// better GPU → better results → more cache reuse → higher weight → more rewards.
//
// The feature is disabled by default and activated via governance.
type CacheQualityParams struct {
	// Enabled gates the entire feature; must be activated via governance before
	// any MsgSubmitCacheQualitySummary transactions are accepted.
	Enabled bool `protobuf:"varint,1,opt,name=enabled,proto3" json:"enabled,omitempty"`
	// ReuseWeightCoefficient is a Decimal multiplier: each cache reuse contributes
	// this many nonce-equivalents toward the participant's epoch weight.
	// Default: 0.1  (i.e., 10 reuses ≡ 1 standard PoC nonce unit).
	ReuseWeightCoefficient *Decimal `protobuf:"bytes,2,opt,name=reuse_weight_coefficient,json=reuseWeightCoefficient,proto3" json:"reuse_weight_coefficient,omitempty"`
	// MaxWeightFractionBps caps how much CacheQualityWeight can add, expressed
	// as a fraction of the participant's standard PoC nonce count (in basis
	// points, where 10 000 = 100%).
	// Default: 3000 (30%) — the cache layer cannot exceed 30% of PoC weight.
	MaxWeightFractionBps uint32 `protobuf:"varint,3,opt,name=max_weight_fraction_bps,json=maxWeightFractionBps,proto3" json:"max_weight_fraction_bps,omitempty"`
	// SimilarityThresholdBps is the minimum cosine similarity × 10 000 required
	// for a semantic match to be counted as a valid cache hit.
	// Default: 9700 (0.97) — tight semantic equivalence required.
	SimilarityThresholdBps uint32 `protobuf:"varint,4,opt,name=similarity_threshold_bps,json=similarityThresholdBps,proto3" json:"similarity_threshold_bps,omitempty"`
	// PruningEpochThreshold is the number of epochs after which
	// CacheQualityEpochSummary records are eligible for pruning.
	// Default: 4.
	PruningEpochThreshold int64 `protobuf:"varint,5,opt,name=pruning_epoch_threshold,json=pruningEpochThreshold,proto3" json:"pruning_epoch_threshold,omitempty"`
	// EmbeddingModelVersion is the governance-managed identifier of the embedding
	// model ALL nodes must use when computing semantic similarity vectors.
	// Changing this via governance immediately invalidates all cached results
	// produced with a different model version (model mismatch = cache miss).
	// Without a shared model version, cosine similarity scores from different
	// models are incomparable and cannot be trusted for cross-node audit.
	// Default: "v1" (all-MiniLM-L6-v2 first stable deployment).
	EmbeddingModelVersion string `protobuf:"bytes,6,opt,name=embedding_model_version,json=embeddingModelVersion,proto3" json:"embedding_model_version,omitempty"`
	// MaxCacheAgeEpochs is the maximum age (in epochs) of a cached result that
	// may be served to users.  Results older than this are treated as cache
	// misses regardless of similarity, preventing stale data accumulation in the
	// off-chain Qdrant vector store.  Unlike PruningEpochThreshold (which cleans
	// on-chain records), this controls off-chain TTL enforcement in the
	// decentralized-api layer via CachedResult.ValidUntilEpoch.
	// Default: 10 epochs (~33 minutes at 5s blocks / 40-block epochs).
	MaxCacheAgeEpochs uint64 `protobuf:"varint,7,opt,name=max_cache_age_epochs,json=maxCacheAgeEpochs,proto3" json:"max_cache_age_epochs,omitempty"`
}

func (m *CacheQualityParams) Reset()         { *m = CacheQualityParams{} }
func (m *CacheQualityParams) String() string { return fmt.Sprintf("%+v", *m) }
func (m *CacheQualityParams) ProtoMessage()  {}

func (m *CacheQualityParams) Equal(other *CacheQualityParams) bool {
	if m == nil && other == nil {
		return true
	}
	if m == nil || other == nil {
		return false
	}
	if m.Enabled != other.Enabled {
		return false
	}
	if !m.ReuseWeightCoefficient.Equal(other.ReuseWeightCoefficient) {
		return false
	}
	if m.MaxWeightFractionBps != other.MaxWeightFractionBps {
		return false
	}
	if m.SimilarityThresholdBps != other.SimilarityThresholdBps {
		return false
	}
	if m.PruningEpochThreshold != other.PruningEpochThreshold {
		return false
	}
	if m.EmbeddingModelVersion != other.EmbeddingModelVersion {
		return false
	}
	if m.MaxCacheAgeEpochs != other.MaxCacheAgeEpochs {
		return false
	}
	return true
}

func (m *CacheQualityParams) Validate() error {
	if m == nil {
		return nil
	}
	if m.MaxWeightFractionBps > 10000 {
		return fmt.Errorf("cache_quality_params: max_weight_fraction_bps must be in [0, 10000], got %d", m.MaxWeightFractionBps)
	}
	if m.SimilarityThresholdBps > 10000 {
		return fmt.Errorf("cache_quality_params: similarity_threshold_bps must be in [0, 10000], got %d", m.SimilarityThresholdBps)
	}
	if m.PruningEpochThreshold < 0 {
		return fmt.Errorf("cache_quality_params: pruning_epoch_threshold must be non-negative, got %d", m.PruningEpochThreshold)
	}
	if m.EmbeddingModelVersion == "" {
		return fmt.Errorf("cache_quality_params: embedding_model_version must not be empty")
	}
	if m.MaxCacheAgeEpochs == 0 {
		return fmt.Errorf("cache_quality_params: max_cache_age_epochs must be > 0")
	}
	return nil
}

func (m *CacheQualityParams) Marshal() (dAtA []byte, err error) {
	size := m.Size()
	dAtA = make([]byte, size)
	n, err := m.MarshalToSizedBuffer(dAtA[:size])
	if err != nil {
		return nil, err
	}
	return dAtA[:n], nil
}

func (m *CacheQualityParams) MarshalTo(dAtA []byte) (int, error) {
	size := m.Size()
	return m.MarshalToSizedBuffer(dAtA[:size])
}

func (m *CacheQualityParams) MarshalToSizedBuffer(dAtA []byte) (int, error) {
	i := len(dAtA)
	_ = i
	if m.MaxCacheAgeEpochs != 0 {
		i = encodeVarintCacheQuality(dAtA, i, uint64(m.MaxCacheAgeEpochs))
		i--
		dAtA[i] = 0x38 // field 7, varint
	}
	if len(m.EmbeddingModelVersion) > 0 {
		i -= len(m.EmbeddingModelVersion)
		copy(dAtA[i:], m.EmbeddingModelVersion)
		i = encodeVarintCacheQuality(dAtA, i, uint64(len(m.EmbeddingModelVersion)))
		i--
		dAtA[i] = 0x32 // field 6, bytes
	}
	if m.PruningEpochThreshold != 0 {
		i = encodeVarintCacheQuality(dAtA, i, uint64(m.PruningEpochThreshold))
		i--
		dAtA[i] = 0x28 // field 5, varint
	}
	if m.SimilarityThresholdBps != 0 {
		i = encodeVarintCacheQuality(dAtA, i, uint64(m.SimilarityThresholdBps))
		i--
		dAtA[i] = 0x20 // field 4, varint
	}
	if m.MaxWeightFractionBps != 0 {
		i = encodeVarintCacheQuality(dAtA, i, uint64(m.MaxWeightFractionBps))
		i--
		dAtA[i] = 0x18 // field 3, varint
	}
	if m.ReuseWeightCoefficient != nil {
		{
			size, err := m.ReuseWeightCoefficient.MarshalToSizedBuffer(dAtA[:i])
			if err != nil {
				return 0, err
			}
			i -= size
			i = encodeVarintCacheQuality(dAtA, i, uint64(size))
		}
		i--
		dAtA[i] = 0x12 // field 2, bytes
	}
	if m.Enabled {
		i--
		if m.Enabled {
			dAtA[i] = 1
		} else {
			dAtA[i] = 0
		}
		i--
		dAtA[i] = 0x8 // field 1, varint (bool)
	}
	return len(dAtA) - i, nil
}

func (m *CacheQualityParams) Size() (n int) {
	if m == nil {
		return 0
	}
	var l int
	_ = l
	if m.Enabled {
		n += 1 + 1 // tag + bool byte
	}
	if m.ReuseWeightCoefficient != nil {
		l = m.ReuseWeightCoefficient.Size()
		n += 1 + l + sovCacheQuality(uint64(l))
	}
	if m.MaxWeightFractionBps != 0 {
		n += 1 + sovCacheQuality(uint64(m.MaxWeightFractionBps))
	}
	if m.SimilarityThresholdBps != 0 {
		n += 1 + sovCacheQuality(uint64(m.SimilarityThresholdBps))
	}
	if m.PruningEpochThreshold != 0 {
		n += 1 + sovCacheQuality(uint64(m.PruningEpochThreshold))
	}
	l = len(m.EmbeddingModelVersion)
	if l > 0 {
		n += 1 + l + sovCacheQuality(uint64(l))
	}
	if m.MaxCacheAgeEpochs != 0 {
		n += 1 + sovCacheQuality(uint64(m.MaxCacheAgeEpochs))
	}
	return n
}

func (m *CacheQualityParams) Unmarshal(dAtA []byte) error {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		preIndex := iNdEx
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return ErrIntOverflowCacheQuality
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
		case 1: // Enabled
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field Enabled", wireType)
			}
			var v int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowCacheQuality
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
			m.Enabled = bool(v != 0)
		case 2: // ReuseWeightCoefficient
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field ReuseWeightCoefficient", wireType)
			}
			var msglen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowCacheQuality
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				msglen |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if msglen < 0 {
				return ErrInvalidLengthCacheQuality
			}
			postIndex := iNdEx + msglen
			if postIndex < 0 {
				return ErrInvalidLengthCacheQuality
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			if m.ReuseWeightCoefficient == nil {
				m.ReuseWeightCoefficient = &Decimal{}
			}
			if err := m.ReuseWeightCoefficient.Unmarshal(dAtA[iNdEx:postIndex]); err != nil {
				return err
			}
			iNdEx = postIndex
		case 3: // MaxWeightFractionBps
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field MaxWeightFractionBps", wireType)
			}
			m.MaxWeightFractionBps = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowCacheQuality
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.MaxWeightFractionBps |= uint32(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 4: // SimilarityThresholdBps
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field SimilarityThresholdBps", wireType)
			}
			m.SimilarityThresholdBps = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowCacheQuality
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.SimilarityThresholdBps |= uint32(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 5: // PruningEpochThreshold
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field PruningEpochThreshold", wireType)
			}
			m.PruningEpochThreshold = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowCacheQuality
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.PruningEpochThreshold |= int64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 6: // EmbeddingModelVersion
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field EmbeddingModelVersion", wireType)
			}
			var stringLen uint64
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowCacheQuality
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				stringLen |= uint64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			intStringLen := int(stringLen)
			if intStringLen < 0 {
				return ErrInvalidLengthCacheQuality
			}
			postIndex := iNdEx + intStringLen
			if postIndex < 0 {
				return ErrInvalidLengthCacheQuality
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.EmbeddingModelVersion = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		case 7: // MaxCacheAgeEpochs
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field MaxCacheAgeEpochs", wireType)
			}
			m.MaxCacheAgeEpochs = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowCacheQuality
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.MaxCacheAgeEpochs |= uint64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		default:
			iNdEx = preIndex
			skippy, err := skipCacheQuality(dAtA[iNdEx:])
			if err != nil {
				return err
			}
			if (skippy < 0) || (iNdEx+skippy) < 0 {
				return ErrInvalidLengthCacheQuality
			}
			if (iNdEx + skippy) > l {
				return io.ErrUnexpectedEOF
			}
			iNdEx += skippy
		}
	}
	if iNdEx > l {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// ── CacheQualityEpochSummary ───────────────────────────────────────────────────
//
// CacheQualityEpochSummary is submitted once per epoch by each participant that
// operated as a semantic cache provider. The protocol stores it keyed by
// (epoch_index, participant_address) and uses CacheQualityWeight at settlement.
type CacheQualityEpochSummary struct {
	ParticipantAddress string `protobuf:"bytes,1,opt,name=participant_address,json=participantAddress,proto3" json:"participant_address,omitempty"`
	EpochIndex         uint64 `protobuf:"varint,2,opt,name=epoch_index,json=epochIndex,proto3" json:"epoch_index,omitempty"`
	// CacheReuseCount is how many times this participant's BLS-signed cached
	// inference results were served to users during this epoch instead of
	// triggering a new GPU computation.
	CacheReuseCount int64 `protobuf:"varint,3,opt,name=cache_reuse_count,json=cacheReuseCount,proto3" json:"cache_reuse_count,omitempty"`
	// OriginalComputeCount is how many new inferences seeded the cache this epoch;
	// together with CacheReuseCount it shows the cache effectiveness ratio.
	OriginalComputeCount int64 `protobuf:"varint,4,opt,name=original_compute_count,json=originalComputeCount,proto3" json:"original_compute_count,omitempty"`
	// AvgSimilarityBps is the average cosine similarity × 10 000 across all cache
	// hits (e.g., 9850 → 0.985 mean similarity).
	AvgSimilarityBps uint32 `protobuf:"varint,5,opt,name=avg_similarity_bps,json=avgSimilarityBps,proto3" json:"avg_similarity_bps,omitempty"`
	// CacheQualityWeight is the pre-computed additional weight contribution for
	// this epoch, set by the msg handler:
	//   min(CacheReuseCount × ReuseWeightCoefficient,
	//       StandardPoCNonces × MaxWeightFractionBps / 10 000)
	// This is the value added to baseCount in calculateParticipantWeight.
	CacheQualityWeight int64 `protobuf:"varint,6,opt,name=cache_quality_weight,json=cacheQualityWeight,proto3" json:"cache_quality_weight,omitempty"`
	// EmbeddingModelVersion records which embedding model version this
	// participant used when computing similarity scores for the epoch.
	// Must match CacheQualityParams.EmbeddingModelVersion at submission time.
	// Stored for auditability: allows verifying that submitted AvgSimilarityBps
	// values are comparable across all participants in this epoch.
	EmbeddingModelVersion string `protobuf:"bytes,7,opt,name=embedding_model_version,json=embeddingModelVersion,proto3" json:"embedding_model_version,omitempty"`
}

func (m *CacheQualityEpochSummary) Reset()         { *m = CacheQualityEpochSummary{} }
func (m *CacheQualityEpochSummary) String() string { return fmt.Sprintf("%+v", *m) }
func (m *CacheQualityEpochSummary) ProtoMessage()  {}

func (m *CacheQualityEpochSummary) Marshal() (dAtA []byte, err error) {
	size := m.Size()
	dAtA = make([]byte, size)
	n, err := m.MarshalToSizedBuffer(dAtA[:size])
	if err != nil {
		return nil, err
	}
	return dAtA[:n], nil
}

func (m *CacheQualityEpochSummary) MarshalTo(dAtA []byte) (int, error) {
	size := m.Size()
	return m.MarshalToSizedBuffer(dAtA[:size])
}

func (m *CacheQualityEpochSummary) MarshalToSizedBuffer(dAtA []byte) (int, error) {
	i := len(dAtA)
	_ = i
	if len(m.EmbeddingModelVersion) > 0 {
		i -= len(m.EmbeddingModelVersion)
		copy(dAtA[i:], m.EmbeddingModelVersion)
		i = encodeVarintCacheQuality(dAtA, i, uint64(len(m.EmbeddingModelVersion)))
		i--
		dAtA[i] = 0x3a // field 7, bytes
	}
	if m.CacheQualityWeight != 0 {
		i = encodeVarintCacheQuality(dAtA, i, uint64(m.CacheQualityWeight))
		i--
		dAtA[i] = 0x30 // field 6, varint
	}
	if m.AvgSimilarityBps != 0 {
		i = encodeVarintCacheQuality(dAtA, i, uint64(m.AvgSimilarityBps))
		i--
		dAtA[i] = 0x28 // field 5, varint
	}
	if m.OriginalComputeCount != 0 {
		i = encodeVarintCacheQuality(dAtA, i, uint64(m.OriginalComputeCount))
		i--
		dAtA[i] = 0x20 // field 4, varint
	}
	if m.CacheReuseCount != 0 {
		i = encodeVarintCacheQuality(dAtA, i, uint64(m.CacheReuseCount))
		i--
		dAtA[i] = 0x18 // field 3, varint
	}
	if m.EpochIndex != 0 {
		i = encodeVarintCacheQuality(dAtA, i, uint64(m.EpochIndex))
		i--
		dAtA[i] = 0x10 // field 2, varint
	}
	if len(m.ParticipantAddress) > 0 {
		i -= len(m.ParticipantAddress)
		copy(dAtA[i:], m.ParticipantAddress)
		i = encodeVarintCacheQuality(dAtA, i, uint64(len(m.ParticipantAddress)))
		i--
		dAtA[i] = 0xa // field 1, bytes
	}
	return len(dAtA) - i, nil
}

func (m *CacheQualityEpochSummary) Size() (n int) {
	if m == nil {
		return 0
	}
	var l int
	_ = l
	l = len(m.ParticipantAddress)
	if l > 0 {
		n += 1 + l + sovCacheQuality(uint64(l))
	}
	if m.EpochIndex != 0 {
		n += 1 + sovCacheQuality(uint64(m.EpochIndex))
	}
	if m.CacheReuseCount != 0 {
		n += 1 + sovCacheQuality(uint64(m.CacheReuseCount))
	}
	if m.OriginalComputeCount != 0 {
		n += 1 + sovCacheQuality(uint64(m.OriginalComputeCount))
	}
	if m.AvgSimilarityBps != 0 {
		n += 1 + sovCacheQuality(uint64(m.AvgSimilarityBps))
	}
	if m.CacheQualityWeight != 0 {
		n += 1 + sovCacheQuality(uint64(m.CacheQualityWeight))
	}
	l = len(m.EmbeddingModelVersion)
	if l > 0 {
		n += 1 + l + sovCacheQuality(uint64(l))
	}
	return n
}

func (m *CacheQualityEpochSummary) Unmarshal(dAtA []byte) error {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		preIndex := iNdEx
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return ErrIntOverflowCacheQuality
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
		case 1: // ParticipantAddress
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field ParticipantAddress", wireType)
			}
			var stringLen uint64
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowCacheQuality
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				stringLen |= uint64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			intStringLen := int(stringLen)
			if intStringLen < 0 {
				return ErrInvalidLengthCacheQuality
			}
			postIndex := iNdEx + intStringLen
			if postIndex < 0 {
				return ErrInvalidLengthCacheQuality
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.ParticipantAddress = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		case 2: // EpochIndex
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field EpochIndex", wireType)
			}
			m.EpochIndex = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowCacheQuality
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
		case 3: // CacheReuseCount
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field CacheReuseCount", wireType)
			}
			m.CacheReuseCount = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowCacheQuality
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.CacheReuseCount |= int64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 4: // OriginalComputeCount
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field OriginalComputeCount", wireType)
			}
			m.OriginalComputeCount = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowCacheQuality
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.OriginalComputeCount |= int64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 5: // AvgSimilarityBps
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field AvgSimilarityBps", wireType)
			}
			m.AvgSimilarityBps = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowCacheQuality
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.AvgSimilarityBps |= uint32(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 6: // CacheQualityWeight
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field CacheQualityWeight", wireType)
			}
			m.CacheQualityWeight = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowCacheQuality
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.CacheQualityWeight |= int64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 7: // EmbeddingModelVersion
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field EmbeddingModelVersion", wireType)
			}
			var stringLen uint64
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowCacheQuality
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				stringLen |= uint64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			intStringLen := int(stringLen)
			if intStringLen < 0 {
				return ErrInvalidLengthCacheQuality
			}
			postIndex := iNdEx + intStringLen
			if postIndex < 0 {
				return ErrInvalidLengthCacheQuality
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.EmbeddingModelVersion = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		default:
			iNdEx = preIndex
			skippy, err := skipCacheQuality(dAtA[iNdEx:])
			if err != nil {
				return err
			}
			if (skippy < 0) || (iNdEx+skippy) < 0 {
				return ErrInvalidLengthCacheQuality
			}
			if (iNdEx + skippy) > l {
				return io.ErrUnexpectedEOF
			}
			iNdEx += skippy
		}
	}
	if iNdEx > l {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// ── MsgSubmitCacheQualitySummary ───────────────────────────────────────────────
//
// MsgSubmitCacheQualitySummary is submitted once per epoch by participants that
// operated as semantic cache providers. The keeper validates the data, computes
// CacheQualityWeight bounded by CacheQualityParams, and stores the summary.
type MsgSubmitCacheQualitySummary struct {
	Creator string                   `protobuf:"bytes,1,opt,name=creator,proto3" json:"creator,omitempty"`
	Summary CacheQualityEpochSummary `protobuf:"bytes,2,opt,name=summary,proto3" json:"summary"`
}

func (m *MsgSubmitCacheQualitySummary) Reset()         { *m = MsgSubmitCacheQualitySummary{} }
func (m *MsgSubmitCacheQualitySummary) String() string { return fmt.Sprintf("%+v", *m) }
func (m *MsgSubmitCacheQualitySummary) ProtoMessage()  {}

// ProtoMarshaler interface compliance.
func (m *MsgSubmitCacheQualitySummary) Marshal() (dAtA []byte, err error) {
	size := m.Size()
	dAtA = make([]byte, size)
	n, err := m.MarshalToSizedBuffer(dAtA[:size])
	if err != nil {
		return nil, err
	}
	return dAtA[:n], nil
}

func (m *MsgSubmitCacheQualitySummary) MarshalTo(dAtA []byte) (int, error) {
	size := m.Size()
	return m.MarshalToSizedBuffer(dAtA[:size])
}

func (m *MsgSubmitCacheQualitySummary) MarshalToSizedBuffer(dAtA []byte) (int, error) {
	i := len(dAtA)
	_ = i
	{
		size, err := m.Summary.MarshalToSizedBuffer(dAtA[:i])
		if err != nil {
			return 0, err
		}
		i -= size
		i = encodeVarintCacheQuality(dAtA, i, uint64(size))
	}
	i--
	dAtA[i] = 0x12 // field 2, bytes
	if len(m.Creator) > 0 {
		i -= len(m.Creator)
		copy(dAtA[i:], m.Creator)
		i = encodeVarintCacheQuality(dAtA, i, uint64(len(m.Creator)))
		i--
		dAtA[i] = 0xa // field 1, bytes
	}
	return len(dAtA) - i, nil
}

func (m *MsgSubmitCacheQualitySummary) Size() (n int) {
	if m == nil {
		return 0
	}
	var l int
	_ = l
	l = len(m.Creator)
	if l > 0 {
		n += 1 + l + sovCacheQuality(uint64(l))
	}
	l = m.Summary.Size()
	n += 1 + l + sovCacheQuality(uint64(l))
	return n
}

func (m *MsgSubmitCacheQualitySummary) Unmarshal(dAtA []byte) error {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		preIndex := iNdEx
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return ErrIntOverflowCacheQuality
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
				return fmt.Errorf("proto: wrong wireType = %d for field Creator", wireType)
			}
			var stringLen uint64
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowCacheQuality
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				stringLen |= uint64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			intStringLen := int(stringLen)
			if intStringLen < 0 {
				return ErrInvalidLengthCacheQuality
			}
			postIndex := iNdEx + intStringLen
			if postIndex < 0 {
				return ErrInvalidLengthCacheQuality
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.Creator = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		case 2:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Summary", wireType)
			}
			var msglen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowCacheQuality
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				msglen |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if msglen < 0 {
				return ErrInvalidLengthCacheQuality
			}
			postIndex := iNdEx + msglen
			if postIndex < 0 {
				return ErrInvalidLengthCacheQuality
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			if err := m.Summary.Unmarshal(dAtA[iNdEx:postIndex]); err != nil {
				return err
			}
			iNdEx = postIndex
		default:
			iNdEx = preIndex
			skippy, err := skipCacheQuality(dAtA[iNdEx:])
			if err != nil {
				return err
			}
			if (skippy < 0) || (iNdEx+skippy) < 0 {
				return ErrInvalidLengthCacheQuality
			}
			if (iNdEx + skippy) > l {
				return io.ErrUnexpectedEOF
			}
			iNdEx += skippy
		}
	}
	if iNdEx > l {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// MsgSubmitCacheQualitySummaryResponse is the empty response to MsgSubmitCacheQualitySummary.
type MsgSubmitCacheQualitySummaryResponse struct{}

func (m *MsgSubmitCacheQualitySummaryResponse) Reset()         {}
func (m *MsgSubmitCacheQualitySummaryResponse) String() string { return "" }
func (m *MsgSubmitCacheQualitySummaryResponse) ProtoMessage()  {}

func (m *MsgSubmitCacheQualitySummaryResponse) Marshal() ([]byte, error)              { return nil, nil }
func (m *MsgSubmitCacheQualitySummaryResponse) MarshalTo([]byte) (int, error)         { return 0, nil }
func (m *MsgSubmitCacheQualitySummaryResponse) MarshalToSizedBuffer([]byte) (int, error) {
	return 0, nil
}
func (m *MsgSubmitCacheQualitySummaryResponse) Size() int { return 0 }
func (m *MsgSubmitCacheQualitySummaryResponse) Unmarshal([]byte) error { return nil }

// SDK message interface compliance.
func (msg *MsgSubmitCacheQualitySummary) Route() string { return ModuleName }
func (msg *MsgSubmitCacheQualitySummary) Type() string  { return "SubmitCacheQualitySummary" }
func (msg *MsgSubmitCacheQualitySummary) ValidateBasic() error {
	if msg.Creator == "" {
		return ErrInvalidAddress.Wrap("creator must not be empty")
	}
	if msg.Summary.CacheReuseCount < 0 {
		return ErrCacheQualityInvalidCount.Wrap("cache_reuse_count must be non-negative")
	}
	if msg.Summary.OriginalComputeCount < 0 {
		return ErrCacheQualityInvalidCount.Wrap("original_compute_count must be non-negative")
	}
	if msg.Summary.AvgSimilarityBps > 10000 {
		return ErrCacheQualityInvalidSimilarity.Wrapf("avg_similarity_bps must be in [0, 10000], got %d", msg.Summary.AvgSimilarityBps)
	}
	if msg.Summary.EmbeddingModelVersion == "" {
		return ErrInvalidAddress.Wrap("summary.embedding_model_version must not be empty")
	}
	return nil
}
func (msg *MsgSubmitCacheQualitySummary) GetSigners() [][]byte {
	return nil // handled by cosmos-sdk address resolution
}

// ── helpers ────────────────────────────────────────────────────────────────────

func encodeVarintCacheQuality(dAtA []byte, offset int, v uint64) int {
	offset -= sovCacheQuality(v)
	base := offset
	for v >= 1<<7 {
		dAtA[offset] = uint8(v&0x7f | 0x80)
		v >>= 7
		offset++
	}
	dAtA[offset] = uint8(v)
	return base
}

func sovCacheQuality(x uint64) (n int) {
	return (math_bits.Len64(x|1) + 6) / 7
}

func skipCacheQuality(dAtA []byte) (n int, err error) {
	l := len(dAtA)
	iNdEx := 0
	depth := 0
	for iNdEx < l {
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return 0, ErrIntOverflowCacheQuality
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
					return 0, ErrIntOverflowCacheQuality
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
					return 0, ErrIntOverflowCacheQuality
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
				return 0, ErrInvalidLengthCacheQuality
			}
			iNdEx += length
		case 3:
			depth++
		case 4:
			if depth == 0 {
				return 0, ErrUnexpectedEndOfGroupCacheQuality
			}
			depth--
		case 5:
			iNdEx += 4
		default:
			return 0, fmt.Errorf("proto: illegal wireType %d", wireType)
		}
		if iNdEx < 0 {
			return 0, ErrInvalidLengthCacheQuality
		}
		if depth == 0 {
			return iNdEx, nil
		}
	}
	return 0, io.ErrUnexpectedEOF
}

var (
	ErrInvalidLengthCacheQuality        = fmt.Errorf("proto: negative length found during unmarshaling")
	ErrIntOverflowCacheQuality          = fmt.Errorf("proto: integer overflow")
	ErrUnexpectedEndOfGroupCacheQuality = fmt.Errorf("proto: unexpected end of group")
)
