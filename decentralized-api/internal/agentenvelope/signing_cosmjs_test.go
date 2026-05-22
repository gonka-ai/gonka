package agentenvelope

import (
	"encoding/base64"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
)

// The constants below are an ADR-036 signature fixture produced by an
// independent implementation: the Node script in
// testdata/adr036_cosmjs_fixture.mjs, which uses @cosmjs/amino and
// @cosmjs/crypto. That amino signing path is the one Keplr signArbitrary also
// uses, so a signature accepted here is accepted for a Keplr-signed envelope.
//
// The script signs with the fixed private key 0x11 repeated 32 times over the
// UTF-8 data "aps-cosmjs-cross-check". See the script header to regenerate.
const (
	cosmjsPubkeyB64    = "A081W9y3zAr3KO88zrlhXZBoS7Wyyl+FmrDwtwQHWHGq"
	cosmjsSigner       = "gonka1l3e9pgs3mmwuwrh95fecme0s0qtn2880el49wm"
	cosmjsDataB64      = "YXBzLWNvc21qcy1jcm9zcy1jaGVjaw=="
	cosmjsSignDocB64   = "eyJhY2NvdW50X251bWJlciI6IjAiLCJjaGFpbl9pZCI6IiIsImZlZSI6eyJhbW91bnQiOltdLCJnYXMiOiIwIn0sIm1lbW8iOiIiLCJtc2dzIjpbeyJ0eXBlIjoic2lnbi9Nc2dTaWduRGF0YSIsInZhbHVlIjp7ImRhdGEiOiJZWEJ6TFdOdmMyMXFjeTFqY205emN5MWphR1ZqYXc9PSIsInNpZ25lciI6ImdvbmthMWwzZTlwZ3MzbW13dXdyaDk1ZmVjbWUwczBxdG4yODgwZWw0OXdtIn19XSwic2VxdWVuY2UiOiIwIn0="
	cosmjsSignatureB64 = "Q616U7VuZ48FGTyu+GUMsZ87yR1MQTUW2GeTQTJzq0caZlFO+0iGdLZ7KnxkYpLorBmRsrj8WLnte6ENi2juPw=="
)

// TestADR036_CosmjsCrossCheck verifies that this package's ADR-036
// construction matches an independent implementation and accepts a signature
// it produced.
func TestADR036_CosmjsCrossCheck(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString(cosmjsDataB64)
	if err != nil {
		t.Fatal(err)
	}

	// Our ADR-036 sign-doc bytes must be byte-identical to the bytes the
	// independent amino serializer produced.
	got, err := adr036SignBytes(cosmjsSigner, data)
	if err != nil {
		t.Fatal(err)
	}
	wantDoc, err := base64.StdEncoding.DecodeString(cosmjsSignDocB64)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(wantDoc) {
		t.Fatalf("ADR-036 sign bytes diverge from cosmjs:\n got:  %s\n want: %s", got, wantDoc)
	}

	// A signature produced by the independent signer must verify through our
	// verification path.
	pkBytes, err := base64.StdEncoding.DecodeString(cosmjsPubkeyB64)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.StdEncoding.DecodeString(cosmjsSignatureB64)
	if err != nil {
		t.Fatal(err)
	}
	pk := &secp256k1.PubKey{Key: pkBytes}
	if !pk.VerifySignature(got, sig) {
		t.Error("cosmjs-produced ADR-036 signature did not verify through VerifySignature")
	}
}
