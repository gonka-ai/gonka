package transport

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"subnet/signing"
)

const (
	// HeaderSignature is the HTTP header carrying the request signature.
	HeaderSignature = "X-Subnet-Signature"
	// HeaderTimestamp is the HTTP header carrying the unix timestamp.
	HeaderTimestamp = "X-Subnet-Timestamp"

	// maxTimestampDrift is the maximum allowed clock skew in seconds.
	maxTimestampDrift = 30
)

// signatureMessage builds the message to sign: sha256(escrow_id || body || timestamp_be8).
func signatureMessage(escrowID string, body []byte, ts int64) []byte {
	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(ts))

	h := sha256.New()
	h.Write([]byte(escrowID))
	h.Write(body)
	h.Write(tsBuf[:])
	return h.Sum(nil)
}

// SignRequest signs the request body with the signer's key.
// Returns the raw signature bytes.
func SignRequest(signer signing.Signer, escrowID string, body []byte, ts int64) ([]byte, error) {
	msg := signatureMessage(escrowID, body, ts)
	return signer.Sign(msg)
}

// VerifyRequest verifies a signed request and returns the recovered sender address.
// Returns an error if the signature is invalid or the timestamp is outside +-maxTimestampDrift.
func VerifyRequest(verifier signing.Verifier, escrowID string, body []byte, sig []byte, ts, now int64) (string, error) {
	drift := ts - now
	if drift < 0 {
		drift = -drift
	}
	if drift > maxTimestampDrift {
		return "", fmt.Errorf("timestamp drift %ds exceeds maximum %ds", drift, maxTimestampDrift)
	}

	msg := signatureMessage(escrowID, body, ts)
	addr, err := verifier.RecoverAddress(msg, sig)
	if err != nil {
		return "", fmt.Errorf("recover address: %w", err)
	}
	return addr, nil
}

