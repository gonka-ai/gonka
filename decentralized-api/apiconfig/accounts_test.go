package apiconfig

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/stretchr/testify/assert"
)

func TestApiAccount_DirectAccess(t *testing.T) {
	// Valid 33-byte secp256k1 public key
	pubKeyBytes := make([]byte, 33)
	pubKeyBytes[0] = 0x03 // compressed key prefix
	pubKey := &secp256k1.PubKey{Key: pubKeyBytes}

	apiAccount := &ApiAccount{
		AccountKey:    pubKey,
		AddressPrefix: "gonka",
	}

	// Direct access to fields
	assert.Equal(t, pubKey, apiAccount.AccountKey)
	assert.Equal(t, "gonka", apiAccount.AddressPrefix)
}
