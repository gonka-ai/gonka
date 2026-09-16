package harness

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRouterCatalogHealthzURL(t *testing.T) {
	require.Equal(t, "http://127.0.0.1:9/v2/healthz",
		routerCatalogHealthzURL("http://127.0.0.1:9", "v2"))
	require.Equal(t, "http://127.0.0.1:9/v2/healthz",
		routerCatalogHealthzURL("http://127.0.0.1:9/", "v2"))
}

func TestGatewayStatusHasRuntime(t *testing.T) {
	require.False(t, gatewayStatusHasRuntime(nil))
	require.False(t, gatewayStatusHasRuntime(map[string]any{}))
	require.False(t, gatewayStatusHasRuntime(map[string]any{"runtimes": float64(0)}))
	require.False(t, gatewayStatusHasRuntime(map[string]any{"devshards": []any{}}))

	require.True(t, gatewayStatusHasRuntime(map[string]any{"escrow_id": "1"}))
	require.True(t, gatewayStatusHasRuntime(map[string]any{"runtimes": float64(1)}))
	require.True(t, gatewayStatusHasRuntime(map[string]any{"devshards": []any{map[string]any{"id": "1"}}}))
}

func TestGatewayStatusHeightSeedReady(t *testing.T) {
	require.True(t, gatewayStatusHeightSeedReady(nil))
	require.True(t, gatewayStatusHeightSeedReady(map[string]any{}))
	require.True(t, gatewayStatusHeightSeedReady(map[string]any{"height_seed": map[string]any{"state": "ok"}}))
	require.False(t, gatewayStatusHeightSeedReady(map[string]any{"height_seed": map[string]any{"state": "pending"}}))
	require.False(t, gatewayStatusHeightSeedReady(map[string]any{"height_seed": map[string]any{"state": "catalog_pending"}}))
	require.False(t, gatewayStatusHeightSeedReady(map[string]any{"height_seed": map[string]any{"state": "missed"}}))
	require.False(t, gatewayStatusHeightSeedReady(map[string]any{
		"height_seed": map[string]any{"state": "ok"},
		"devshards":   []any{map[string]any{"height_seed": map[string]any{"state": "pending"}}},
	}))
	require.True(t, gatewayStatusHeightSeedReady(map[string]any{
		"devshards": []any{map[string]any{"height_seed": map[string]any{"state": "ok"}}},
	}))
}
