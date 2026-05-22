package agentenvelope

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"decentralized-api/internal/agentenvelope/jcs"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
)

// sampleEnvelopeJSON returns minimal envelope-shaped JSON for signing tests.
// Full schema validation is exercised in the verifier tests.
func sampleEnvelopeJSON(principalSig string) []byte {
	return []byte(`{"v":"aps-agent-envelope-v1","audience":"gonka",` +
		`"chain_id":"gonka-test","agent_id":"urn:aps:agent:t",` +
		`"principal_sig":"` + principalSig + `"}`)
}

// signPrincipal produces an ADR-036 signature over an envelope, the way a
// principal SDK would. It returns the standard-base64 signature.
func signPrincipal(t *testing.T, priv *secp256k1.PrivKey, signer string, rawEnvelope []byte) string {
	t.Helper()
	canon, err := principalCanonicalJSON(rawEnvelope)
	if err != nil {
		t.Fatalf("principalCanonicalJSON: %v", err)
	}
	signBytes, err := adr036SignBytes(signer, canon)
	if err != nil {
		t.Fatalf("adr036SignBytes: %v", err)
	}
	sig, err := priv.Sign(signBytes)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

func TestVerifyPrincipalSig_RoundTrip(t *testing.T) {
	priv := secp256k1.GenPrivKey()
	pub := priv.PubKey().(*secp256k1.PubKey)
	addr := deriveAddress(pub)

	env := sampleEnvelopeJSON("")
	sigB64 := signPrincipal(t, priv, addr, env)

	if err := VerifyPrincipalSig(pub, addr, env, sigB64); err != nil {
		t.Fatalf("round-trip verify failed: %v", err)
	}
	// The same signature verifies when principal_sig is already populated,
	// since canonicalization empties it.
	filled := sampleEnvelopeJSON(sigB64)
	if err := VerifyPrincipalSig(pub, addr, filled, sigB64); err != nil {
		t.Errorf("verify against populated principal_sig failed: %v", err)
	}
}

func TestVerifyPrincipalSig_WrongKeyFails(t *testing.T) {
	priv := secp256k1.GenPrivKey()
	pub := priv.PubKey().(*secp256k1.PubKey)
	addr := deriveAddress(pub)
	env := sampleEnvelopeJSON("")
	sigB64 := signPrincipal(t, priv, addr, env)

	other := secp256k1.GenPrivKey().PubKey().(*secp256k1.PubKey)
	if err := VerifyPrincipalSig(other, addr, env, sigB64); err != ErrPrincipalSigInvalid {
		t.Errorf("got %v, want ErrPrincipalSigInvalid", err)
	}
}

func TestVerifyPrincipalSig_WrongSignerAddressFails(t *testing.T) {
	priv := secp256k1.GenPrivKey()
	pub := priv.PubKey().(*secp256k1.PubKey)
	addr := deriveAddress(pub)
	env := sampleEnvelopeJSON("")
	sigB64 := signPrincipal(t, priv, addr, env)

	// The ADR-036 message binds the signer address. Verifying with a
	// different address changes the sign bytes and must fail.
	otherAddr := deriveAddress(secp256k1.GenPrivKey().PubKey().(*secp256k1.PubKey))
	if err := VerifyPrincipalSig(pub, otherAddr, env, sigB64); err != ErrPrincipalSigInvalid {
		t.Errorf("got %v, want ErrPrincipalSigInvalid", err)
	}
}

func TestVerifyPrincipalSig_TamperedEnvelopeFails(t *testing.T) {
	priv := secp256k1.GenPrivKey()
	pub := priv.PubKey().(*secp256k1.PubKey)
	addr := deriveAddress(pub)
	env := sampleEnvelopeJSON("")
	sigB64 := signPrincipal(t, priv, addr, env)

	tampered := []byte(`{"v":"aps-agent-envelope-v1","audience":"gonka",` +
		`"chain_id":"gonka-OTHER","agent_id":"urn:aps:agent:t","principal_sig":""}`)
	if err := VerifyPrincipalSig(pub, addr, tampered, sigB64); err != ErrPrincipalSigInvalid {
		t.Errorf("got %v, want ErrPrincipalSigInvalid", err)
	}
}

func TestVerifyPrincipalSig_BadBase64Fails(t *testing.T) {
	pub := secp256k1.GenPrivKey().PubKey().(*secp256k1.PubKey)
	if err := VerifyPrincipalSig(pub, "addr", sampleEnvelopeJSON(""), "not!base64!"); err != ErrPrincipalSigInvalid {
		t.Errorf("got %v, want ErrPrincipalSigInvalid", err)
	}
}

func TestADR036SignBytes_ReferenceVector(t *testing.T) {
	// Pins the ADR-036 sign-doc to the documented amino-JSON form. Confirmed
	// against cosmos-sdk legacytx.StdSignDoc: account_number and sequence are
	// amino-marshaled as the string "0", timeout_height is the only omitempty
	// field (so it is absent), and chain_id, memo, and fee are always present.
	got, err := adr036SignBytes("gonka1test", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"account_number":"0","chain_id":"","fee":{"amount":[],"gas":"0"},` +
		`"memo":"","msgs":[{"type":"sign/MsgSignData","value":` +
		`{"data":"aGVsbG8=","signer":"gonka1test"}}],"sequence":"0"}`
	if string(got) != want {
		t.Errorf("ADR-036 sign bytes diverged from amino-JSON form:\n got: %s\nwant: %s", got, want)
	}
}

func TestAgentSigPayload_LengthPrefixUnambiguous(t *testing.T) {
	// Two field tuples whose naive concatenation would collide must produce
	// distinct payloads under length prefixing.
	a := AgentSigPayload("ab", "", "/p", nil, nil)
	b := AgentSigPayload("a", "b", "/p", nil, nil)
	if string(a) == string(b) {
		t.Error("length prefixing failed to disambiguate field boundaries")
	}
}

func TestVerifyAgentSig_RoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	env := sampleEnvelopeJSON("sig")
	body := []byte(`{"model":"Qwen/QwQ-32B"}`)

	canon, err := jcs.Canonicalize(env)
	if err != nil {
		t.Fatal(err)
	}
	payload := AgentSigPayload("gonka-test", "POST", "/v1/chat/completions", canon, body)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))

	if err := VerifyAgentSig(pub, "gonka-test", "POST", "/v1/chat/completions", env, body, sig); err != nil {
		t.Fatalf("round-trip verify failed: %v", err)
	}
}

func TestVerifyAgentSig_TamperedBodyFails(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	env := sampleEnvelopeJSON("sig")
	canon, err := jcs.Canonicalize(env)
	if err != nil {
		t.Fatal(err)
	}
	payload := AgentSigPayload("gonka-test", "POST", "/v1/chat/completions", canon, []byte("original"))
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))

	if err := VerifyAgentSig(pub, "gonka-test", "POST", "/v1/chat/completions", env, []byte("tampered"), sig); err != ErrAgentSigInvalid {
		t.Errorf("got %v, want ErrAgentSigInvalid", err)
	}
}

func TestVerifyAgentSig_TamperedPathFails(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	env := sampleEnvelopeJSON("sig")
	canon, err := jcs.Canonicalize(env)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("b")
	payload := AgentSigPayload("gonka-test", "POST", "/v1/chat/completions", canon, body)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))

	if err := VerifyAgentSig(pub, "gonka-test", "POST", "/v1/completions", env, body, sig); err != ErrAgentSigInvalid {
		t.Errorf("got %v, want ErrAgentSigInvalid", err)
	}
}
