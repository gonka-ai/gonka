package queryapi

import (
	"encoding/json"
	"testing"

	"common/utils"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cosmosed25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	blstypes "github.com/productscience/inference/x/bls/types"
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

	// Legacy dapi (encoding/json on comet types.Validator) emitted numbers.
	require.Equal(t, int64(100), m["voting_power"])
	require.Equal(t, int64(-7), m["proposer_priority"])

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

func TestProtoToAPIJSON_EnumNamesAndNumericInts(t *testing.T) {
	raw, err := protoToAPIJSON(&blstypes.EpochBLSData{
		EpochId:                     7,
		DkgPhase:                    blstypes.DKGPhase_DKG_PHASE_COMPLETED,
		DealingPhaseDeadlineBlock:   100,
		VerifyingPhaseDeadlineBlock: 103,
	})
	require.NoError(t, err)
	m, ok := raw.(map[string]any)
	require.True(t, ok)

	require.Equal(t, uint64(7), m["epoch_id"])
	require.Equal(t, int64(100), m["dealing_phase_deadline_block"])
	phase, ok := m["dkg_phase"].(string)
	require.True(t, ok, "dkg_phase must be an enum name, got %T %v", m["dkg_phase"], m["dkg_phase"])
	require.Contains(t, phase, "COMPLETED")
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
