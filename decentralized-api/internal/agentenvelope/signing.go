package agentenvelope

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"

	"decentralized-api/internal/agentenvelope/jcs"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
)

// agentSigDomain is the domain-separation tag prefixed to every agent signing
// payload. A fixed protocol tag means an APS agent signature cannot be
// mistaken for a signature in any other protocol, and the reverse.
const agentSigDomain = "APS-AGENT-SIG-V1\n"

// principalCanonicalJSON returns the JCS-canonical form of the envelope used
// for the ADR-036 principal signature: the received envelope with the
// principal_sig field set to the empty string. The field is present and
// empty, never absent, so the signer and verifier canonicalize the same shape.
func principalCanonicalJSON(rawEnvelope []byte) ([]byte, error) {
	var m map[string]interface{}
	if err := json.Unmarshal(rawEnvelope, &m); err != nil {
		return nil, ErrEnvelopeMalformed
	}
	m["principal_sig"] = ""
	re, err := json.Marshal(m)
	if err != nil {
		return nil, ErrEnvelopeMalformed
	}
	return jcs.Canonicalize(re)
}

// adr036SignBytes builds the ADR-036 arbitrary-data sign bytes for a signer
// over data.
//
// The structure is the legacy-amino StdSignDoc that Keplr signArbitrary and
// the cosmos-sdk CLI tx sign-data both produce for off-chain signing: an empty
// chain_id, account_number and sequence of 0, an empty fee, no memo, and a
// single sign/MsgSignData message carrying the signer address and the
// base64-encoded data. account_number, sequence and gas are JSON strings, and
// fee.amount is an empty array, matching amino JSON.
//
// The cosmos-sdk fork in use does not ship a turnkey MsgSignData type, so the
// document is constructed directly and canonicalized with the same RFC 8785
// JCS used elsewhere. Amino JSON sorts object keys, which JCS also does, so
// the output matches the bytes a Keplr or CLI signer hashes and signs.
func adr036SignBytes(signer string, data []byte) ([]byte, error) {
	doc := map[string]interface{}{
		"account_number": "0",
		"chain_id":       "",
		"fee":            map[string]interface{}{"amount": []interface{}{}, "gas": "0"},
		"memo":           "",
		"msgs": []interface{}{
			map[string]interface{}{
				"type": "sign/MsgSignData",
				"value": map[string]interface{}{
					"data":   base64.StdEncoding.EncodeToString(data),
					"signer": signer,
				},
			},
		},
		"sequence": "0",
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return jcs.Canonicalize(raw)
}

// PrincipalSignBytes returns the ADR-036 bytes the principal signs to
// authorize an envelope. A client computes these bytes, signs them with the
// principal's secp256k1 key, and places the standard-base64 signature in the
// envelope's principal_sig field. The principal_sig field is emptied before
// canonicalization, so the caller may pass the envelope JSON with that field
// absent, empty, or already populated.
func PrincipalSignBytes(principalAddress string, rawEnvelope []byte) ([]byte, error) {
	canon, err := principalCanonicalJSON(rawEnvelope)
	if err != nil {
		return nil, err
	}
	return adr036SignBytes(principalAddress, canon)
}

// VerifyPrincipalSig verifies the ADR-036 principal signature over the
// envelope. pk is the secp256k1 key embedded in the envelope. principalAddress
// is bound into the ADR-036 message, so a signature is valid only for the
// address it was produced for.
//
// Verification goes through secp256k1.PubKey.VerifySignature, which expects
// the 64-byte R||S low-S Cosmos signature form and applies the SHA-256 prehash
// internally. The Go ecdsa package is never used directly: it would produce
// DER-encoded signatures that do not verify here.
func VerifyPrincipalSig(pk *secp256k1.PubKey, principalAddress string, rawEnvelope []byte, sigB64 string) error {
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return ErrPrincipalSigInvalid
	}
	signBytes, err := PrincipalSignBytes(principalAddress, rawEnvelope)
	if err != nil {
		return err
	}
	if !pk.VerifySignature(signBytes, sig) {
		return ErrPrincipalSigInvalid
	}
	return nil
}

// AgentSigPayload builds the length-prefixed, domain-separated payload an agent
// signs for one request:
//
//	"APS-AGENT-SIG-V1\n" ||
//	u32_be(len(chain_id))     || chain_id ||
//	u32_be(len(method))       || method   ||
//	u32_be(len(path))         || path     ||
//	u32_be(len(envelope_jcs)) || envelope_jcs ||
//	u32_be(len(body))         || body
//
// Length prefixes make field boundaries unambiguous: no two distinct field
// tuples can serialize to the same byte string, which a plain separator could
// allow. This is the TLS 1.3 transcript and Noise protocol pattern. The body
// is bounded well below 4 GiB by the body-size limit, so the uint32 length is
// always exact.
func AgentSigPayload(chainID, method, path string, envelopeJCS, body []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString(agentSigDomain)
	writeLengthPrefixed(&buf, []byte(chainID))
	writeLengthPrefixed(&buf, []byte(method))
	writeLengthPrefixed(&buf, []byte(path))
	writeLengthPrefixed(&buf, envelopeJCS)
	writeLengthPrefixed(&buf, body)
	return buf.Bytes()
}

func writeLengthPrefixed(buf *bytes.Buffer, b []byte) {
	var lp [4]byte
	binary.BigEndian.PutUint32(lp[:], uint32(len(b)))
	buf.Write(lp[:])
	buf.Write(b)
}

// VerifyAgentSig verifies the per-request Ed25519 agent signature over the
// length-prefixed payload. The agent key signs the payload directly with pure
// Ed25519 (RFC 8032), with no SHA-256 prehash: a prehash would be a
// non-standard construction.
func VerifyAgentSig(pub ed25519.PublicKey, chainID, method, path string, rawEnvelope, body []byte, sigB64 string) error {
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrAgentSigInvalid
	}
	envelopeJCS, err := jcs.Canonicalize(rawEnvelope)
	if err != nil {
		return ErrEnvelopeMalformed
	}
	payload := AgentSigPayload(chainID, method, path, envelopeJCS, body)
	if !ed25519.Verify(pub, payload, sig) {
		return ErrAgentSigInvalid
	}
	return nil
}
