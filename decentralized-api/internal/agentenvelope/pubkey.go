package agentenvelope

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// secp256k1CompressedLen is the byte length of a compressed secp256k1 public key.
const secp256k1CompressedLen = 33

// decodeAgentPubkey validates an agent TypedKey and returns the ed25519 key.
// The agent key is always ed25519: it is the portable, network-agnostic
// per-request signing key.
func decodeAgentPubkey(tk TypedKey) (ed25519.PublicKey, error) {
	if tk.Type != KeyTypeEd25519 {
		return nil, ErrAgentPubkeyTypeInvalid
	}
	raw, err := base64.StdEncoding.DecodeString(tk.PublicKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, ErrAgentPubkeyMalformed
	}
	return ed25519.PublicKey(raw), nil
}

// decodePrincipalPubkey validates a principal TypedKey and returns the
// secp256k1 key. The principal key is always secp256k1: it anchors to a
// Gonka account, whose identity model is secp256k1.
//
// The type is asserted strictly. Multi-signature principal keys are a real
// Cosmos primitive but break the single-key signing assumption, so v1 rejects
// them with a distinct error rather than failing later in an opaque way. This
// is the algorithm-confusion defense from architectural principle P13.
func decodePrincipalPubkey(tk TypedKey) (*secp256k1.PubKey, error) {
	if strings.Contains(strings.ToLower(tk.Type), "multisig") {
		return nil, ErrPrincipalMultisigUnsupported
	}
	if tk.Type != KeyTypeSecp256k1 {
		return nil, ErrPrincipalPubkeyTypeInvalid
	}
	raw, err := base64.StdEncoding.DecodeString(tk.PublicKey)
	if err != nil || len(raw) != secp256k1CompressedLen {
		return nil, ErrPrincipalPubkeyMalformed
	}
	return &secp256k1.PubKey{Key: raw}, nil
}

// deriveAddress returns the bech32 account address for a secp256k1 public key,
// using the SDK's configured account prefix.
//
// Derivation is bech32(ripemd160(sha256(compressed_pubkey))). It is one-way
// verifiable: the verifier requires the derived address to equal the
// envelope's principal_address. A signature verified by a pubkey that derives
// to the stated address is equivalent to looking up the same pubkey on chain,
// so embedding the pubkey in the envelope adds no trust assumption while
// removing the chain query entirely (architectural principle P3).
func deriveAddress(pk *secp256k1.PubKey) string {
	return sdk.AccAddress(pk.Address()).String()
}
