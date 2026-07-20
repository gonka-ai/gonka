package queryapi

import (
	"encoding/json"
	"testing"

	"common/utils"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cosmosed25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/stretchr/testify/require"
)

func TestValidatorsToRawJSON_FlattensPubKeyToBase64String(t *testing.T) {
	pk := cosmosed25519.PubKey{Key: bytes32("01234567890123456789012345678901")}
	anyPK, err := codectypes.NewAnyWithValue(&pk)
	require.NoError(t, err)

	val := &cmtservice.Validator{
		Address:          "gonkavalcons1ignored",
		PubKey:           anyPK,
		VotingPower:      100,
		ProposerPriority: -7,
	}

	out, err := validatorsToRawJSON([]*cmtservice.Validator{val})
	require.NoError(t, err)
	require.Len(t, out, 1)

	m, ok := out[0].(map[string]any)
	require.True(t, ok)

	pubKey, ok := m["pub_key"].(string)
	require.True(t, ok, "pub_key must be a base64 string, got %T", m["pub_key"])
	require.Equal(t, utils.PubKeyToString(&pk), pubKey)

	address, ok := m["address"].(string)
	require.True(t, ok)
	expectedAddr, err := utils.ValidatorKeyToHexAddress(pubKey)
	require.NoError(t, err)
	require.Equal(t, expectedAddr, address)

	require.Equal(t, "100", m["voting_power"])
	require.Equal(t, "-7", m["proposer_priority"])

	b, err := json.Marshal(out[0])
	require.NoError(t, err)
	require.NotContains(t, string(b), "@type")
}

func TestProtoToRawJSON_ValidatorWithPubKeyAny(t *testing.T) {
	pk := cosmosed25519.PubKey{Key: bytes32("01234567890123456789012345678901")}
	anyPK, err := codectypes.NewAnyWithValue(&pk)
	require.NoError(t, err)

	val := &cmtservice.Validator{
		Address:     "gonkavalcons1test",
		PubKey:      anyPK,
		VotingPower: 100,
	}

	_, err = validatorsToRawJSON([]*cmtservice.Validator{val})
	require.NoError(t, err)

	raw, err := protoToRawJSON(val)
	require.NoError(t, err)

	b, err := json.Marshal(raw)
	require.NoError(t, err)
	require.Contains(t, string(b), "pub_key")
}

func bytes32(s string) []byte {
	b := []byte(s)
	if len(b) < 32 {
		padded := make([]byte, 32)
		copy(padded, b)
		return padded
	}
	return b[:32]
}
