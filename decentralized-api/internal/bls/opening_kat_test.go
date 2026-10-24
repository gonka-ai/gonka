package bls

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/require"
)

// openingKATCiphertext is the ciphertext of a fixed (key, share, seed). The chain pins the same
// bytes in inference-chain/x/bls/keeper/dispute_opening_kat_test.go.
//
// Complaint adjudication re-encrypts the dealer's revealed (share, seed) on chain and requires
// byte equality with the ciphertext this package produced. The seed reaches the ECIES ephemeral
// key through ecdsa.GenerateKey, which ignores a custom reader since Go 1.26 unless
// GODEBUG=cryptocustomrand=1; that default follows the go directive in go.mod. After such a bump
// ciphertexts still decrypt, so DKG and the other tests keep passing, but no honest dealer can
// answer a complaint any more. Both sides must keep producing these bytes.
const openingKATCiphertext = "04d6bb28122f03418d5ea4ba4637fa9e717569b9c2afe2278ed9ad6b4f37e8c7067e8d685c142ce3bf13d29e1f23cbe60ec5357c70b73de7c27fa243c11be724906ee0eeb9c93fd303f9bcedf9fc6f28a0ba7c9d3d6f937a3849696909b604e20a985f0c781547f37dd7e8d6f0c7c9ddb557d51ce2748ecfb80541ec9eabcd2499b176f6292ecbceb1d97103216f7249dd"

func TestEncryptForParticipantWithSeed_KnownAnswer(t *testing.T) {
	pub := secp256k1.PrivKeyFromBytes(bytes.Repeat([]byte{7}, 32)).PubKey().SerializeCompressed()
	share := bytes.Repeat([]byte{0x11}, 32)
	seed := bytes.Repeat([]byte{0x42}, dkgOpeningSeedLen)

	first, err := encryptForParticipantWithSeed(share, pub, seed)
	require.NoError(t, err)
	second, err := encryptForParticipantWithSeed(share, pub, seed)
	require.NoError(t, err)
	require.Equal(t, first, second, "same seed must give the same ciphertext")
	require.Equal(t, openingKATCiphertext, hex.EncodeToString(first))
}
