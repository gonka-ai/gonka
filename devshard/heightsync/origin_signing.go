package heightsync

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	"devshard/signing"
	"devshard/types"
)

// Domain separator for response-leg origin signatures (spec §15).
const OriginSignDomain = "heightsync.origin.v1"

var (
	ErrOriginSignEmptySection = errors.New("heightsync: empty section for origin signing")
	ErrOriginSignNoSignature  = errors.New("heightsync: missing sender_signature")
	ErrOriginSignVerify       = errors.New("heightsync: origin signature verification failed")
)

// sectionToProto maps a HeightSyncSection to protobuf fields 1–7 (no sender_signature).
func sectionToProto(sec *HeightSyncSection) *types.InferenceHeightSyncSection {
	if sec == nil {
		return nil
	}
	pt := types.InferenceHeightSyncProofType_INFERENCE_HEIGHT_SYNC_PROOF_TYPE_UNSPECIFIED
	if sec.ProofType == AnchorProofType {
		pt = types.InferenceHeightSyncProofType_INFERENCE_HEIGHT_SYNC_PROOF_TYPE_HEIGHT_ANCHOR_V1
	}
	resp := sec.Direction == "response"
	return &types.InferenceHeightSyncSection{
		ProofType:                 pt,
		MainnetHeight:             sec.MainnetHeight,
		MainnetBlockHashHex:       sec.MainnetBlockHashHex,
		TimestampUnixMs:           sec.TimestampUnixMs,
		Response:                  resp,
		OriginatorSenderId:        sec.OriginatorSenderID,
		OriginatorTimestampUnixMs: sec.OriginatorTimestampMs,
	}
}

// CanonicalOriginBytes returns domain-separated protobuf-canonical bytes for fields 1–7.
func CanonicalOriginBytes(sec *HeightSyncSection) ([]byte, error) {
	if sec == nil || !IsAnchorSection(sec) {
		return nil, ErrOriginSignEmptySection
	}
	body, err := proto.Marshal(sectionToProto(sec))
	if err != nil {
		return nil, fmt.Errorf("marshal origin section: %w", err)
	}
	return append([]byte(OriginSignDomain), body...), nil
}

// SignOrigin signs sec and returns (canonical_blob, signature).
func SignOrigin(signer signing.Signer, sec *HeightSyncSection) (blob, sig []byte, err error) {
	if signer == nil {
		return nil, nil, errors.New("heightsync: nil signer")
	}
	blob, err = CanonicalOriginBytes(sec)
	if err != nil {
		return nil, nil, err
	}
	sig, err = signer.Sign(blob)
	if err != nil {
		return nil, nil, fmt.Errorf("sign origin: %w", err)
	}
	return blob, sig, nil
}

// VerifyOrigin checks sender_signature against originator_sender_id using verifier.
func VerifyOrigin(verifier signing.Verifier, sec *HeightSyncSection, sig []byte) error {
	if verifier == nil {
		return errors.New("heightsync: nil verifier")
	}
	if sec == nil || !IsAnchorSection(sec) {
		return ErrOriginSignEmptySection
	}
	if len(sig) == 0 {
		return ErrOriginSignNoSignature
	}
	origin := strings.TrimSpace(sec.OriginatorSenderID)
	if origin == "" {
		return fmt.Errorf("%w: empty originator_sender_id", ErrOriginSignVerify)
	}
	blob, err := CanonicalOriginBytes(sec)
	if err != nil {
		return err
	}
	recovered, err := verifier.RecoverAddress(blob, sig)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrOriginSignVerify, err)
	}
	if recovered != origin {
		return fmt.Errorf("%w: signer %q != originator %q", ErrOriginSignVerify, recovered, origin)
	}
	return nil
}

// VerifyOriginDetached checks a detached blob/signature pair.
func VerifyOriginDetached(verifier signing.Verifier, sec *HeightSyncSection, blob, sig []byte) error {
	if len(blob) == 0 || len(sig) == 0 {
		return ErrOriginSignNoSignature
	}
	expect, err := CanonicalOriginBytes(sec)
	if err != nil {
		return err
	}
	if !bytes.Equal(expect, blob) {
		return fmt.Errorf("%w: blob mismatch", ErrOriginSignVerify)
	}
	return VerifyOrigin(verifier, sec, sig)
}
