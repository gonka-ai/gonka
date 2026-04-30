package types

import (
	"encoding/binary"
	"fmt"
)

const (
	// ModuleName defines the module name
	ModuleName = "bls"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// MemStoreKey defines the in-memory store key
	MemStoreKey = "mem_bls"
)

var (
	ParamsKey                     = []byte("p_bls")
	EpochBLSDataPrefix            = []byte("epoch_bls_data")
	DealerPartPrefix              = []byte("epoch_bls_dealer_part/")
	VerificationSubmissionPrefix  = []byte("epoch_bls_verification_submission/")
	DealerComplaintPrefix         = []byte("epoch_bls_dealer_complaint/")
	ThresholdPartialSigPrefix     = []byte("threshold_partial_sig/")
	ThresholdSigningRequestPrefix = []byte("threshold_signing_request")
	ExpirationIndexPrefix         = []byte("expiration_index")
	GroupValidationPrefix         = []byte("group_validation_")
	// Deliberately does NOT start with GroupValidationPrefix so that a
	// prefix.Store scoped to GroupValidationPrefix (used in genesis export
	// and elsewhere) does not yield partial-sig entries. Keep this invariant
	// in mind if renaming either prefix.
	GroupValidationPartialSigPrefix = []byte("bls_partial_sig/")
	CompletedPostProcessRetryPrefix = []byte("completed_post_process_retry")
)

func KeyPrefix(p string) []byte {
	return []byte(p)
}

// EpochBLSDataKey generates a key for storing EpochBLSData by epoch ID
func EpochBLSDataKey(epochID uint64) []byte {
	key := make([]byte, len(EpochBLSDataPrefix)+8)
	copy(key, EpochBLSDataPrefix)
	binary.BigEndian.PutUint64(key[len(EpochBLSDataPrefix):], epochID)
	return key
}

// DealerPartEpochPrefix returns the prefix used to iterate all dealer parts
// for a given epoch ID. Callers wrap the module KV store in a prefix.Store
// scoped to this prefix, then use DealerPartSubKey for per-participant
// point access.
//
// Storing each dealer part under its own key prevents the entire
// EpochBLSData struct from being rewritten on every MsgSubmitDealerPart
// submission. Before this change, the Nth dealer paid gas proportional to N
// (because the dealing struct accumulated dealer parts inline), which
// created a race where the DAPI's simulation-based gas estimate was too low
// by the time the tx landed, pushing later dealers over the declared gas
// limit and failing them out of the DKG entirely.
//
// Full layout: {DealerPartPrefix}{epoch_id:uint64 BE}/{participant_index:uint32 BE}.
func DealerPartEpochPrefix(epochID uint64) []byte {
	prefix := make([]byte, len(DealerPartPrefix)+8+1)
	copy(prefix, DealerPartPrefix)
	binary.BigEndian.PutUint64(prefix[len(DealerPartPrefix):], epochID)
	prefix[len(DealerPartPrefix)+8] = '/'
	return prefix
}

// DealerPartSubKey returns the sub-key portion of a dealer part entry (the
// bytes under DealerPartEpochPrefix). This is what callers use when working
// through a prefix.Store scoped to a single epoch.
func DealerPartSubKey(participantIndex uint32) []byte {
	sub := make([]byte, 4)
	binary.BigEndian.PutUint32(sub, participantIndex)
	return sub
}

// ParseDealerPartSubKey decodes a sub-key produced by DealerPartSubKey back
// into a participant index.
func ParseDealerPartSubKey(sub []byte) (uint32, error) {
	if len(sub) != 4 {
		return 0, fmt.Errorf("invalid dealer part sub-key length %d (want 4)", len(sub))
	}
	return binary.BigEndian.Uint32(sub), nil
}

// VerificationSubmissionEpochPrefix returns the prefix used to iterate all
// verification vector submissions for a given epoch ID. Same shape as
// DealerPartEpochPrefix — a prefix.Store scoped to this prefix yields one
// entry per participant that has submitted a verification vector, sub-keyed
// by participant index.
//
// This split exists for the same reason as the dealer-part split: a single
// EpochBLSData base struct that holds all verification submissions inline
// grows O(N) per verifier and causes the same WritePerByte gas-scaling
// race that kicks later verifiers out of the round.
//
// Full layout: {VerificationSubmissionPrefix}{epoch_id:uint64 BE}/{participant_index:uint32 BE}.
func VerificationSubmissionEpochPrefix(epochID uint64) []byte {
	prefix := make([]byte, len(VerificationSubmissionPrefix)+8+1)
	copy(prefix, VerificationSubmissionPrefix)
	binary.BigEndian.PutUint64(prefix[len(VerificationSubmissionPrefix):], epochID)
	prefix[len(VerificationSubmissionPrefix)+8] = '/'
	return prefix
}

// VerificationSubmissionSubKey returns the sub-key portion of a verification
// submission entry (the bytes under VerificationSubmissionEpochPrefix).
func VerificationSubmissionSubKey(participantIndex uint32) []byte {
	sub := make([]byte, 4)
	binary.BigEndian.PutUint32(sub, participantIndex)
	return sub
}

// ParseVerificationSubmissionSubKey decodes a sub-key produced by
// VerificationSubmissionSubKey back into a participant index.
func ParseVerificationSubmissionSubKey(sub []byte) (uint32, error) {
	if len(sub) != 4 {
		return 0, fmt.Errorf("invalid verification submission sub-key length %d (want 4)", len(sub))
	}
	return binary.BigEndian.Uint32(sub), nil
}

// DealerComplaintEpochPrefix returns the prefix used to iterate all dealer
// complaints collected during an epoch's verifying phase. The sub-key within
// the returned store is the compound (dealer_index, complainer_index) pair
// so (dealer, complainer) uniquely identifies a complaint entry.
//
// Same rationale as the other splits: the verifier handler used to append
// every complaint into a single inline slice on EpochBLSData, so the Nth
// verifier's tx re-serialized every prior verifier's complaints. Per-entry
// sub-keys make each complaint's write cost constant.
//
// Full layout: {DealerComplaintPrefix}{epoch_id:uint64 BE}/{dealer_index:uint32 BE}{complainer_index:uint32 BE}.
func DealerComplaintEpochPrefix(epochID uint64) []byte {
	prefix := make([]byte, len(DealerComplaintPrefix)+8+1)
	copy(prefix, DealerComplaintPrefix)
	binary.BigEndian.PutUint64(prefix[len(DealerComplaintPrefix):], epochID)
	prefix[len(DealerComplaintPrefix)+8] = '/'
	return prefix
}

// DealerComplaintSubKey returns the sub-key portion of a dealer complaint
// entry (the bytes under DealerComplaintEpochPrefix). Fixed-width encoding
// keeps the sort order deterministic and the parser trivial.
func DealerComplaintSubKey(dealerIndex, complainerIndex uint32) []byte {
	sub := make([]byte, 8)
	binary.BigEndian.PutUint32(sub[0:4], dealerIndex)
	binary.BigEndian.PutUint32(sub[4:8], complainerIndex)
	return sub
}

// ParseDealerComplaintSubKey decodes a sub-key produced by
// DealerComplaintSubKey back into (dealer_index, complainer_index).
func ParseDealerComplaintSubKey(sub []byte) (uint32, uint32, error) {
	if len(sub) != 8 {
		return 0, 0, fmt.Errorf("invalid dealer complaint sub-key length %d (want 8)", len(sub))
	}
	dealerIndex := binary.BigEndian.Uint32(sub[0:4])
	complainerIndex := binary.BigEndian.Uint32(sub[4:8])
	return dealerIndex, complainerIndex, nil
}

// ThresholdPartialSigRequestPrefix returns the prefix used to iterate all
// partial signatures collected for a single threshold signing request. The
// request ID is variable length, so it is preceded by a 4-byte length
// prefix and followed by a '/' separator to make the prefix unambiguous
// across requests whose ID bytes are a subset of each other.
//
// A prefix.Store scoped to this prefix yields one entry per submitter for
// the given request, keyed by the submitter address bytes.
//
// Pre-split, ThresholdSigningRequest.PartialSignatures accumulated inline
// and the Nth signer paid gas proportional to N prior signers. Splitting
// per-submitter makes each write constant-cost.
//
// Full layout: {ThresholdPartialSigPrefix}{request_id_len:uint32 BE}{request_id}/{submitter_bytes}.
func ThresholdPartialSigRequestPrefix(requestID []byte) []byte {
	buf := make([]byte, len(ThresholdPartialSigPrefix)+4+len(requestID)+1)
	pos := copy(buf, ThresholdPartialSigPrefix)
	binary.BigEndian.PutUint32(buf[pos:], uint32(len(requestID)))
	pos += 4
	pos += copy(buf[pos:], requestID)
	buf[pos] = '/'
	return buf
}

// ThresholdPartialSigSubKey returns the sub-key portion of a threshold
// partial signature entry. Sub-keys are the submitter's account-address
// bytes — one submission per participant per request is enforced by the
// handler, so the address uniquely identifies the entry.
func ThresholdPartialSigSubKey(submitter string) []byte {
	return []byte(submitter)
}

// ThresholdSigningRequestKey generates a key for storing ThresholdSigningRequest by request ID
// This results in a variable length key, as we put no constraints on the request_id
func ThresholdSigningRequestKey(requestID []byte) []byte {
	key := make([]byte, len(ThresholdSigningRequestPrefix)+len(requestID))
	copy(key, ThresholdSigningRequestPrefix)
	copy(key[len(ThresholdSigningRequestPrefix):], requestID)
	return key
}

// ExpirationIndexKey generates a key for the expiration index: expiration_index/{deadline_block_height}/{request_id}
func ExpirationIndexKey(deadlineBlockHeight int64, requestID []byte) []byte {
	deadlineBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(deadlineBytes, uint64(deadlineBlockHeight))

	key := make([]byte, len(ExpirationIndexPrefix)+8+len(requestID))
	copy(key, ExpirationIndexPrefix)
	copy(key[len(ExpirationIndexPrefix):], deadlineBytes)
	copy(key[len(ExpirationIndexPrefix)+8:], requestID)
	return key
}

// ExpirationIndexPrefixForBlock generates a prefix to scan all requests expiring at a specific block height
func ExpirationIndexPrefixForBlock(blockHeight int64) []byte {
	deadlineBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(deadlineBytes, uint64(blockHeight))

	prefix := make([]byte, len(ExpirationIndexPrefix)+8)
	copy(prefix, ExpirationIndexPrefix)
	copy(prefix[len(ExpirationIndexPrefix):], deadlineBytes)
	return prefix
}

// GroupValidationKey generates a key for the group validation state by epoch ID
func GroupValidationKey(epochID uint64) []byte {
	return []byte(fmt.Sprintf("%s%d", GroupValidationPrefix, epochID))
}

// GroupValidationPartialSigEpochPrefix returns the prefix used to iterate all
// per-participant partial signatures collected for a single new-epoch
// validation round. Callers wrap the module KV store in a prefix.Store scoped
// to this prefix, then use GroupValidationPartialSigSubKey for per-participant
// point access.
//
// Storing each partial signature under its own key prevents the entire
// GroupKeyValidationState from being rewritten on every
// MsgSubmitGroupKeyValidationSignature. Before this change the Nth signer
// paid gas proportional to N (because PartialSignatures accumulated inline),
// creating the same simulate-vs-execute out-of-gas race that PR #1070 fixed
// for dealer parts.
//
// Full layout: {GroupValidationPartialSigPrefix}{new_epoch_id:uint64 BE}/{participant_index:uint32 BE}.
func GroupValidationPartialSigEpochPrefix(newEpochID uint64) []byte {
	prefix := make([]byte, len(GroupValidationPartialSigPrefix)+8+1)
	copy(prefix, GroupValidationPartialSigPrefix)
	binary.BigEndian.PutUint64(prefix[len(GroupValidationPartialSigPrefix):], newEpochID)
	prefix[len(GroupValidationPartialSigPrefix)+8] = '/'
	return prefix
}

// GroupValidationPartialSigSubKey returns the sub-key portion of a partial
// signature entry (the bytes under GroupValidationPartialSigEpochPrefix).
func GroupValidationPartialSigSubKey(participantIndex uint32) []byte {
	sub := make([]byte, 4)
	binary.BigEndian.PutUint32(sub, participantIndex)
	return sub
}

// ParseGroupValidationPartialSigSubKey decodes a sub-key produced by
// GroupValidationPartialSigSubKey back into a participant index.
func ParseGroupValidationPartialSigSubKey(sub []byte) (uint32, error) {
	if len(sub) != 4 {
		return 0, fmt.Errorf("invalid group validation partial sig sub-key length %d (want 4)", len(sub))
	}
	return binary.BigEndian.Uint32(sub), nil
}

func CompletedPostProcessRetryKey(requestID []byte) []byte {
	key := make([]byte, len(CompletedPostProcessRetryPrefix)+len(requestID))
	copy(key, CompletedPostProcessRetryPrefix)
	copy(key[len(CompletedPostProcessRetryPrefix):], requestID)
	return key
}
