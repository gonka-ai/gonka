package testutil

import "testing"

func TestGatewayStatusHeightSeedReady(t *testing.T) {
	if !gatewayStatusHeightSeedReady(nil) {
		t.Fatal("nil status is ready (gate off)")
	}
	if !gatewayStatusHeightSeedReady(map[string]any{}) {
		t.Fatal("empty status is ready")
	}
	if !gatewayStatusHeightSeedReady(map[string]any{"height_seed": map[string]any{"state": "ok"}}) {
		t.Fatal("ok is ready")
	}
	if gatewayStatusHeightSeedReady(map[string]any{"height_seed": map[string]any{"state": "pending"}}) {
		t.Fatal("pending is not ready")
	}
	if gatewayStatusHeightSeedReady(map[string]any{"height_seed": map[string]any{"state": "catalog_pending"}}) {
		t.Fatal("catalog_pending is not ready")
	}
}
