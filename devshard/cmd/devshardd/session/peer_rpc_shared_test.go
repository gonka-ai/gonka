package session

import (
	"testing"

	"github.com/stretchr/testify/require"

	"common/storage/mode"

	"devshard/storage"
)

func TestPeerRPCSharedEnabled(t *testing.T) {
	t.Setenv(mode.EnvHADeployment, "")
	require.False(t, peerRPCSharedEnabled(true, "v2"), "unset GONKA_HA stays in memory")

	for _, raw := range []string{"false", "off", "0", "no"} {
		t.Setenv(mode.EnvHADeployment, raw)
		require.False(t, peerRPCSharedEnabled(true, "v2"), raw)
	}

	t.Setenv(mode.EnvHADeployment, "true")
	require.False(t, peerRPCSharedEnabled(false, "v2"), "sqlite has no shared pool")
	require.False(t, peerRPCSharedEnabled(true, " "), "a child with no version is not an HA member")
	require.True(t, peerRPCSharedEnabled(true, "v2"))

	t.Setenv(mode.EnvHADeployment, "on")
	require.True(t, peerRPCSharedEnabled(true, "v2"))

	t.Setenv(mode.EnvHADeployment, "nope")
	require.False(t, peerRPCSharedEnabled(true, "v2"), "an invalid flag must not enable the shared store")
}

func TestPeerRPCSharedMemoryStaysReadyWhenHA(t *testing.T) {
	t.Setenv(mode.EnvHADeployment, "true")
	mgr := NewHostManager(storage.NewMemory(), mustGenerateKey(t), nil, nil, nil, "v2", nil, nil, nil)
	t.Cleanup(func() { _ = mgr.Close() })
	mgr.SetRPCServerEnabled(true)

	h := mgr.peerAuthHandler()
	require.NotNil(t, h)
	require.True(t, h.SessionsReady(), "HA without Postgres must not wait on the shared store")
	require.True(t, mgr.PeerRPCSessionsReady())
}
