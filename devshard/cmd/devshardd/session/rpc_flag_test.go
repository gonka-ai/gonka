package session

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/observability"
	"devshard/storage"
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
