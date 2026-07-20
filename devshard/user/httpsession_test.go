package user

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"devshard/bridge"
	"devshard/storage"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

func TestNewHTTPSessionRequiresRoutePrefix(t *testing.T) {
	_, _, err := NewHTTPSession(HTTPSessionConfig{})
	require.ErrorContains(t, err, "RoutePrefix is required")
	require.ErrorContains(t, err, "/devshard/{version}")
	require.False(t, errors.Is(err, ErrLocalStateUnrecoverable))
}

func TestNewHTTPSessionOpenStorageFailureStaysFatal(t *testing.T) {
	const privateKeyHex = "0000000000000000000000000000000000000000000000000000000000000001"
	storagePath := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(storagePath, []byte("occupied"), 0o600))

	_, _, err := NewHTTPSession(HTTPSessionConfig{
		PrivateKeyHex:   privateKeyHex,
		EscrowID:        "escrow-1",
		Bridge:          httpsessionTestBridge{},
		StoragePath:     storagePath,
		RoutePrefix:     "/devshard/dev",
		ProtocolVersion: types.ProtocolV1,
	})

	// Open errors are environmental (permissions, disk), not proven history
	// corruption: they must not carry the deactivate-and-skip sentinel.
	require.Error(t, err)
	require.ErrorContains(t, err, "open storage")
	require.False(t, errors.Is(err, ErrLocalStateUnrecoverable))
}

func TestNewHTTPSessionClassifiesNonSequentialReplay(t *testing.T) {
	const privateKeyHex = "0000000000000000000000000000000000000000000000000000000000000001"
	storagePath := filepath.Join(t.TempDir(), "session")
	store, err := storage.NewSQLite(storagePath)
	require.NoError(t, err)
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		EpochID:        7,
		Version:        "dev",
		CreatorAddr:    "creator",
		Group:          []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "host-1"}},
		InitialBalance: 1_000_000,
	}))
	require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{Diff: types.Diff{Nonce: 1}}))
	require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{Diff: types.Diff{Nonce: 3}}))
	require.NoError(t, store.Close())

	_, _, err = NewHTTPSession(HTTPSessionConfig{
		PrivateKeyHex:   privateKeyHex,
		EscrowID:        "escrow-1",
		Bridge:          httpsessionTestBridge{},
		StoragePath:     storagePath,
		RoutePrefix:     "/devshard/dev",
		ProtocolVersion: types.ProtocolV1,
	})

	require.ErrorIs(t, err, ErrLocalStateUnrecoverable)
	require.ErrorContains(t, err, "recover session")
	require.ErrorContains(t, err, "missing nonce 2, next stored nonce is 3")
}

func TestDeferredWarmKeyResolverAvoidsExternalCallsDuringRecovery(t *testing.T) {
	calls := 0
	resolver, enable := deferredWarmKeyResolver(func(_, _ string) (bool, error) {
		calls++
		return true, nil
	})

	ok, err := resolver("warm", "cold")
	require.NoError(t, err)
	require.False(t, ok)
	require.Zero(t, calls)

	enable()
	ok, err = resolver("warm", "cold")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 1, calls)
}

func TestNewHTTPSessionUsesRouteVersionForStorageBind(t *testing.T) {
	const privateKeyHex = "0000000000000000000000000000000000000000000000000000000000000001"
	storagePath := t.TempDir() + "/session.db"

	session, _, err := NewHTTPSession(HTTPSessionConfig{
		PrivateKeyHex:   privateKeyHex,
		EscrowID:        "escrow-1",
		Bridge:          httpsessionTestBridge{},
		StoragePath:     storagePath,
		RoutePrefix:     " /devshard/dev/ ",
		ProtocolVersion: types.ProtocolV1,
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
