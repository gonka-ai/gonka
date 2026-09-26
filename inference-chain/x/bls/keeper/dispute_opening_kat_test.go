package keeper

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/require"
)

// openingKATCiphertext is what the dealer in decentralized-api/internal/bls produces for a fixed
// (key, share, seed); decentralized-api/internal/bls/opening_kat_test.go pins the same bytes.
//
// verifyDealerComplaintResponse accepts a dealer's opening only if encryptWithSeedForParticipant
// reproduces the stored ciphertext byte for byte. The seed reaches the ECIES ephemeral key through
// ecdsa.GenerateKey, which ignores a custom reader since Go 1.26 unless GODEBUG=cryptocustomrand=1;
// that default follows the go directive in go.mod. After such a bump on either side every honest
// opening fails adjudication while decryption and the rest of DKG keep working.
const openingKATCiphertext = "04d6bb28122f03418d5ea4ba4637fa9e717569b9c2afe2278ed9ad6b4f37e8c7067e8d685c142ce3bf13d29e1f23cbe60ec5357c70b73de7c27fa243c11be724906ee0eeb9c93fd303f9bcedf9fc6f28a0ba7c9d3d6f937a3849696909b604e20a985f0c781547f37dd7e8d6f0c7c9ddb557d51ce2748ecfb80541ec9eabcd2499b176f6292ecbceb1d97103216f7249dd"

func TestEncryptWithSeedForParticipant_KnownAnswer(t *testing.T) {
	pub := secp256k1.PrivKeyFromBytes(bytes.Repeat([]byte{7}, 32)).PubKey().SerializeCompressed()
	share := bytes.Repeat([]byte{0x11}, 32)
	seed := bytes.Repeat([]byte{0x42}, dkgOpeningSeedLen)

	ciphertext, err := encryptWithSeedForParticipant(share, pub, seed)
	require.NoError(t, err)
	require.Equal(t, openingKATCiphertext, hex.EncodeToString(ciphertext))
}
