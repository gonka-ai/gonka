package user

import (
	"testing"

	"devshard/bridge"
	"devshard/storage"

	"github.com/stretchr/testify/require"
)

func TestNewHTTPSessionRequiresRoutePrefix(t *testing.T) {
	_, _, err := NewHTTPSession(HTTPSessionConfig{})
	require.ErrorContains(t, err, "RoutePrefix is required")
	require.ErrorContains(t, err, "/devshard/{version}")
}

func TestNewHTTPSessionUsesRouteVersionForStorageBind(t *testing.T) {
	const privateKeyHex = "0000000000000000000000000000000000000000000000000000000000000001"
	storagePath := t.TempDir() + "/session.db"

	session, _, err := NewHTTPSession(HTTPSessionConfig{
		PrivateKeyHex: privateKeyHex,
		EscrowID:      "escrow-1",
		Bridge:        httpsessionTestBridge{},
		StoragePath:   storagePath,
		RoutePrefix:   " /devshard/dev/ ",
	})
	require.NoError(t, err)
	require.NoError(t, session.Close())

	store, err := storage.NewSQLite(storagePath)
	require.NoError(t, err)
	defer store.Close()

	meta, err := store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, "dev", meta.Version)
}

type httpsessionTestBridge struct{}

func (httpsessionTestBridge) OnEscrowCreated(bridge.EscrowInfo) error { return nil }
func (httpsessionTestBridge) OnSettlementProposed(string, []byte, uint64) error {
	return nil
}
func (httpsessionTestBridge) OnSettlementFinalized(string) error { return nil }

func (httpsessionTestBridge) GetEscrow(string) (*bridge.EscrowInfo, error) {
	return &bridge.EscrowInfo{
		EscrowID:       "escrow-1",
		Amount:         1_000_000,
		CreatorAddress: "creator",
		Slots:          []string{"host-1"},
		TokenPrice:     1,
		EpochID:        7,
	}, nil
}

func (httpsessionTestBridge) GetHostInfo(address string) (*bridge.HostInfo, error) {
	return &bridge.HostInfo{Address: address, URL: "http://host.test"}, nil
}

func (httpsessionTestBridge) GetValidationThreshold(uint64, string) (*bridge.Decimal, error) {
	return nil, nil
}

func (httpsessionTestBridge) VerifyWarmKey(string, string) (bool, error) { return true, nil }
func (httpsessionTestBridge) SubmitDisputeState(string, []byte, uint64, map[uint32][]byte) error {
	return nil
}
