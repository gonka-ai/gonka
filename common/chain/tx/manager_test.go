package tx_test

import (
	"testing"

	"common/chain"
	"common/chain/tx"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/stretchr/testify/require"
)

func TestNew_ValidKeyring(t *testing.T) {
	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	kr := keyring.NewInMemory(cdc)

	chainClient, err := chain.New("localhost:9090")
	require.NoError(t, err)

	mgr, err := tx.New(chainClient.Conn(), kr, "gonka1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqhupgam", "validator")
	require.NoError(t, err)
	require.NotNil(t, mgr)
}
