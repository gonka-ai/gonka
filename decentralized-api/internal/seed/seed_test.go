package seed

import (
	"testing"

	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/stretchr/testify/require"
)

type deterministicSeedSigner struct {
	cosmosclient.MockCosmosMessageClient
	privateKey *secp256k1.PrivKey
}

func (s *deterministicSeedSigner) SignBytes(message []byte) ([]byte, error) {
	return s.privateKey.Sign(message)
}

func TestCreateNewSeedIsDeterministicForEpoch(t *testing.T) {
	signer := &deterministicSeedSigner{privateKey: secp256k1.GenPrivKey()}
	manager := NewRandomSeedManager(signer, &apiconfig.ConfigManager{})

	first, err := manager.CreateNewSeed(344)
	require.NoError(t, err)
	second, err := manager.CreateNewSeed(344)
	require.NoError(t, err)

	require.Equal(t, first.Seed, second.Seed)
	require.Equal(t, first.Signature, second.Signature)
}
