package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/stub"
)

func TestHostManager_CloseHostsLeavesStoreUsable(t *testing.T) {
	store := storage.NewMemory()
	user := mustGenerateKey(t)
	hostKeys := []*signing.Secp256k1Signer{mustGenerateKey(t), mustGenerateKey(t), mustGenerateKey(t)}
	group := makeGroup(hostKeys)
	cfg := defaultConfig(3)
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID: "escrow-close-hosts", EpochID: 1, Version: testutil.RuntimeTestVersion,
		CreatorAddr: user.Address(), Config: cfg, Group: group, InitialBalance: 100000,
	}))

	mgr := NewHostManager(store, hostKeys[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)

	mgr.CloseHosts()
	mgr.CloseHosts() // idempotent

	sessions, err := store.ListActiveSessions()
	require.NoError(t, err)
	assert.NotEmpty(t, sessions, "CloseHosts must not close storage")

	require.NoError(t, mgr.Close())
}
