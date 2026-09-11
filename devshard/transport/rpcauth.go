package transport

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"net/http"

	"devshard/signing"
	"devshard/transport/rpcpb"
)

// SessionHeader is the bearer from Attach. Every RPC except Attach must carry
// it. The value is hex of AttachResponse.session_token.
const SessionHeader = "X-Devshard-Session"

// EncodeSessionToken is the on-wire form of AttachResponse.session_token.
func EncodeSessionToken(token []byte) string {
	return hex.EncodeToString(token)
}

// SetSessionHeader puts the Attach token on a Connect request.
func SetSessionHeader(h http.Header, token []byte) {
	h.Set(SessionHeader, EncodeSessionToken(token))
}

// AttachDomain is the signing domain for PeerAuthService.Attach.
const AttachDomain = "devshard.attach.v1"

// AttachProtocolVersion is the only AttachRequest.protocol_version the server
// accepts. This is the RPC schema (proto package), not the escrow version in the
// URL — that path already selected this child.
const AttachProtocolVersion = "devshard.transport.v1"

// SignEnvelope signs payload with the same byte layout as SignRequest.
func SignEnvelope(signer signing.Signer, escrowID string, payload []byte, ts int64) (*rpcpb.SignedEnvelope, error) {
	sig, err := SignRequest(signer, escrowID, payload, ts)
	if err != nil {
		return nil, err
	}
	return &rpcpb.SignedEnvelope{
		Payload:   payload,
		Signature: sig,
		Timestamp: ts,
		EscrowId:  escrowID,
	}, nil
}

// VerifyEnvelope verifies a SignedEnvelope using VerifyRequest so historical
// HTTP signatures remain valid across the transport switch.
func VerifyEnvelope(verifier signing.Verifier, env *rpcpb.SignedEnvelope, now int64) (string, error) {
	if env == nil {
		return "", fmt.Errorf("nil signed envelope")
	}
	return VerifyRequest(verifier, env.EscrowId, env.Payload, env.Signature, env.Timestamp, now)
}

// AttachSignatureMessage is sha256("devshard.attach.v1" || length-prefixed
// fields). Timestamp is unix seconds as big-endian uint64. Length prefixes
// stop adjacent attacker-controlled fields from shifting into each other.
func AttachSignatureMessage(hostAddress string, ts int64, peerAddress string, attachNonce []byte, protocolVersion string, channelBinding []byte) []byte {
	h := sha256.New()
	h.Write([]byte(AttachDomain))
	writeLP(h, []byte(hostAddress))
	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(ts))
	h.Write(tsBuf[:])
	writeLP(h, []byte(peerAddress))
	writeLP(h, attachNonce)
	writeLP(h, []byte(protocolVersion))
	writeLP(h, channelBinding)
	return h.Sum(nil)
}

func writeLP(h hash.Hash, b []byte) {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b)))
	h.Write(n[:])
	h.Write(b)
}

// SignAttach signs an AttachRequest body.
func SignAttach(signer signing.Signer, hostAddress string, ts int64, peerAddress string, attachNonce []byte, protocolVersion string, channelBinding []byte) ([]byte, error) {
	return signer.Sign(AttachSignatureMessage(hostAddress, ts, peerAddress, attachNonce, protocolVersion, channelBinding))
}

// VerifyAttach recovers the signer of an AttachRequest. now is unix seconds
// for the same ±MaxTimestampDrift window as VerifyRequest.
func VerifyAttach(verifier signing.Verifier, req *rpcpb.AttachRequest, now int64) (string, error) {
	if req == nil {
		return "", fmt.Errorf("nil attach request")
	}
	drift := req.Timestamp - now
	if drift < 0 {
		drift = -drift
	}
	if drift > MaxTimestampDrift {
		return "", fmt.Errorf("timestamp drift %ds exceeds maximum %ds", drift, MaxTimestampDrift)
	}
	msg := AttachSignatureMessage(req.HostAddress, req.Timestamp, req.PeerAddress, req.AttachNonce, req.ProtocolVersion, req.ChannelBinding)
	addr, err := verifier.RecoverAddress(msg, req.Signature)
	if err != nil {
		return "", fmt.Errorf("recover address: %w", err)
	}
	return addr, nil
}
