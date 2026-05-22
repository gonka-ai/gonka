package agentenvelope

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
)

func TestDecodeAgentPubkey_Valid(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tk := TypedKey{Type: KeyTypeEd25519, PublicKey: base64.StdEncoding.EncodeToString(pub)}
	got, err := decodeAgentPubkey(tk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ed25519.PublicKey(got).Equal(pub) {
		t.Error("decoded key does not match input")
	}
}

func TestDecodeAgentPubkey_WrongType(t *testing.T) {
	tk := TypedKey{Type: KeyTypeSecp256k1, PublicKey: base64.StdEncoding.EncodeToString(make([]byte, 32))}
	if _, err := decodeAgentPubkey(tk); err != ErrAgentPubkeyTypeInvalid {
		t.Errorf("got %v, want ErrAgentPubkeyTypeInvalid", err)
	}
}

func TestDecodeAgentPubkey_Malformed(t *testing.T) {
	cases := map[string]TypedKey{
		"bad base64": {Type: KeyTypeEd25519, PublicKey: "not!base64!"},
		"wrong len":  {Type: KeyTypeEd25519, PublicKey: base64.StdEncoding.EncodeToString(make([]byte, 31))},
	}
	for name, tk := range cases {
		if _, err := decodeAgentPubkey(tk); err != ErrAgentPubkeyMalformed {
			t.Errorf("%s: got %v, want ErrAgentPubkeyMalformed", name, err)
		}
	}
}

func TestDecodePrincipalPubkey_Valid(t *testing.T) {
	pk := secp256k1.GenPrivKey().PubKey().(*secp256k1.PubKey)
	tk := TypedKey{Type: KeyTypeSecp256k1, PublicKey: base64.StdEncoding.EncodeToString(pk.Key)}
	got, err := decodePrincipalPubkey(tk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equals(pk) {
		t.Error("decoded key does not match input")
	}
}

func TestDecodePrincipalPubkey_WrongType(t *testing.T) {
	tk := TypedKey{Type: KeyTypeEd25519, PublicKey: base64.StdEncoding.EncodeToString(make([]byte, 33))}
	if _, err := decodePrincipalPubkey(tk); err != ErrPrincipalPubkeyTypeInvalid {
		t.Errorf("got %v, want ErrPrincipalPubkeyTypeInvalid", err)
	}
}

func TestDecodePrincipalPubkey_Multisig(t *testing.T) {
	tk := TypedKey{Type: "tendermint/PubKeyMultisigThreshold", PublicKey: base64.StdEncoding.EncodeToString(make([]byte, 33))}
	if _, err := decodePrincipalPubkey(tk); err != ErrPrincipalMultisigUnsupported {
		t.Errorf("got %v, want ErrPrincipalMultisigUnsupported", err)
	}
}

func TestDecodePrincipalPubkey_Malformed(t *testing.T) {
	cases := map[string]TypedKey{
		"bad base64": {Type: KeyTypeSecp256k1, PublicKey: "not!base64!"},
		"wrong len":  {Type: KeyTypeSecp256k1, PublicKey: base64.StdEncoding.EncodeToString(make([]byte, 32))},
	}
	for name, tk := range cases {
		if _, err := decodePrincipalPubkey(tk); err != ErrPrincipalPubkeyMalformed {
			t.Errorf("%s: got %v, want ErrPrincipalPubkeyMalformed", name, err)
		}
	}
}

func TestDeriveAddress_Deterministic(t *testing.T) {
	pk := secp256k1.GenPrivKey().PubKey().(*secp256k1.PubKey)
	a := deriveAddress(pk)
	b := deriveAddress(pk)
	if a == "" || a != b {
		t.Errorf("address derivation not deterministic: %q vs %q", a, b)
	}
	other := secp256k1.GenPrivKey().PubKey().(*secp256k1.PubKey)
	if deriveAddress(other) == a {
		t.Error("distinct keys derived the same address")
	}
}
