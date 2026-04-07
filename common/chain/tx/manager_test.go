package tx_test

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"common/chain/tx"
)

func TestNew_ValidKeyring(t *testing.T) {
	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	kr := keyring.NewInMemory(cdc)

	conn, err := grpc.NewClient("localhost:9090", grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	mgr, err := tx.New(conn, kr, "gonka1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqhupgam", tx.TxManagerConfig{
		SignerKeyName:  "validator",
		KeyringBackend: "test",
		KeyringDir:     t.TempDir(),
		ChainID:        "inference-1",
	})
	require.NoError(t, err)
	require.NotNil(t, mgr)
}
