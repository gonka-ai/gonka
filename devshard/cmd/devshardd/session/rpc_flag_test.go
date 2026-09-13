package session

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/observability"
	"devshard/storage"
	"devshard/transport/rpcserver"
)

func TestHostManager_RPCRoutesGatedByFlag(t *testing.T) {
	mgr := NewHostManager(storage.NewMemory(), mustGenerateKey(t), nil, nil, nil, "v5", nil, nil, nil)
	t.Cleanup(func() { _ = mgr.Close() })

	eOff := echo.New()
	mgr.Register(eOff.Group(""))
	for _, r := range eOff.Routes() {
		require.NotContains(t, r.Path, "/rpc", r.Method+" "+r.Path)
	}

	mgr.SetRPCServerEnabled(true)
	eOn := echo.New()
	mgr.Register(eOn.Group(""))
	found := false
	for _, r := range eOn.Routes() {
		if strings.Contains(r.Path, "/rpc") {
			found = true
			break
		}
	}
	require.True(t, found, "DEVSHARD_RPC_SERVER_ENABLED must mount /sessions/:id/rpc")
	require.Equal(t, 1.0, prometheusGauge(t, "devshard_peer_rpc_enabled"))
}

func TestHostManager_RPCEnabledEmptyHostPanics(t *testing.T) {
	mgr := NewHostManager(storage.NewMemory(), nil, nil, nil, nil, "v5", nil, nil, nil)
	t.Cleanup(func() { _ = mgr.Close() })
	mgr.SetRPCServerEnabled(true)

	defer func() {
		r := recover()
		require.NotNil(t, r)
		require.Contains(t, fmt.Sprint(r), "host address")
	}()
	mgr.Register(echo.New().Group(""))
}

func TestHostManager_ClosePeerRPCConcurrentWithStart(t *testing.T) {
	mgr := NewHostManager(storage.NewMemory(), mustGenerateKey(t), nil, nil, nil, "v5", nil, nil, nil)
	t.Cleanup(func() { _ = mgr.Close() })

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = mgr.peerAuthHandler()
		}()
		go func() {
			defer wg.Done()
			mgr.ClosePeerRPC()
		}()
	}
	wg.Wait()
	mgr.ClosePeerRPC()
	require.Equal(t, 0.0, prometheusGauge(t, "devshard_peer_rpc_enabled"))
}

func prometheusGauge(t *testing.T, name string) float64 {
	t.Helper()
	families, err := observability.Registry().Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.Metric {
			if m.Gauge != nil {
				return m.Gauge.GetValue()
			}
		}
	}
	return 0
}

func TestAllowRPCPeer_NilServer(t *testing.T) {
	mgr := NewHostManager(storage.NewMemory(), mustGenerateKey(t), nil, nil, nil, "v5", nil, nil, nil)
	t.Cleanup(func() { _ = mgr.Close() })
	mgr.sessionsMutex.Lock()
	mgr.sessions["1"] = nil
	mgr.sessionsMutex.Unlock()

	ok, err := mgr.allowRPCPeer(rpcserver.WithEscrowID(context.Background(), "1"), "gonka1peer")
	require.False(t, ok)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestAllowRPCPeer_OwnerBindsSession(t *testing.T) {
	const escrowID = "9801"
	mgr, store, user, _ := setupBindTestManager(t, escrowID)

	ok, err := mgr.allowRPCPeer(rpcserver.WithEscrowID(context.Background(), escrowID), user.Address())
	require.NoError(t, err)
	require.True(t, ok)

	meta, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err)
	require.Equal(t, user.Address(), meta.CreatorAddr)
}

func TestAllowRPCPeer_GroupMemberDoesNotBind(t *testing.T) {
	const escrowID = "9802"
	mgr, store, _, host0 := setupBindTestManager(t, escrowID)
	slots := mgr.bridge.(*mockBridge).escrow.Slots
	require.GreaterOrEqual(t, len(slots), 2)
	member := slots[1]
	require.NotEqual(t, host0.Address(), member)

	ok, err := mgr.allowRPCPeer(rpcserver.WithEscrowID(context.Background(), escrowID), member)
	require.NoError(t, err)
	require.True(t, ok)

	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestAllowRPCPeer_StrangerDoesNotBind(t *testing.T) {
	const escrowID = "9803"
	mgr, store, _, _ := setupBindTestManager(t, escrowID)
	stranger := mustGenerateKey(t)

	ok, err := mgr.allowRPCPeer(rpcserver.WithEscrowID(context.Background(), escrowID), stranger.Address())
	require.NoError(t, err)
	require.False(t, ok)

	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestAllowRPCPeer_SettledEscrowDoesNotBind(t *testing.T) {
	const escrowID = "9804"
	mgr, store, user, _ := setupBindTestManager(t, escrowID)
	mgr.bridge.(*mockBridge).escrow.Settled = true

	ok, err := mgr.allowRPCPeer(rpcserver.WithEscrowID(context.Background(), escrowID), user.Address())
	require.False(t, ok)
	require.ErrorIs(t, err, bridge.ErrEscrowSettled)

	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestAllowRPCPeer_MemberThenOwnerBinds(t *testing.T) {
	const escrowID = "9805"
	mgr, store, user, _ := setupBindTestManager(t, escrowID)
	member := mgr.bridge.(*mockBridge).escrow.Slots[1]
	ctx := rpcserver.WithEscrowID(context.Background(), escrowID)

	ok, err := mgr.allowRPCPeer(ctx, member)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)

	ok, err = mgr.allowRPCPeer(ctx, user.Address())
	require.NoError(t, err)
	require.True(t, ok)
	meta, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err)
	require.Equal(t, user.Address(), meta.CreatorAddr)
}

func TestAllowRPCPeer_ChainUnavailable(t *testing.T) {
	const escrowID = "9806"
	mgr, store, user, _ := setupBindTestManager(t, escrowID)
	mgr.bridge.(*mockBridge).getEscrowErr = bridge.ErrChainUnavailable

	ok, err := mgr.allowRPCPeer(rpcserver.WithEscrowID(context.Background(), escrowID), user.Address())
	require.False(t, ok)
	require.ErrorIs(t, err, bridge.ErrChainUnavailable)

	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}
