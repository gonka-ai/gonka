package main

import (
	"encoding/json"
	"testing"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// Test flow:
// 1. Fill a default config that carries two escrows, one per model, as the load runner writes for a model mix.
// 2. Check both escrows get the chain fields: creator, slots, amount and app hash.
// 3. Render compose and check gateway gets DEVSHARDS_JSON with one seed per escrow, key by env name only.
func TestWriteCompose_MultiModelEscrows(t *testing.T) {
	cfg := defaultConfig()
	cfg.Escrows = []config.Escrow{{ID: 1, ModelID: "model-a"}, {ID: 2, ModelID: "model-b"}}
	require.NoError(t, fillConfig(cfg))
	for _, escrow := range cfg.Escrows {
		require.Equal(t, cfg.User.Address, escrow.Creator, escrow.ModelID)
		require.Equal(t, cfg.Escrows[0].Slots, escrow.Slots, escrow.ModelID)
		require.NotZero(t, escrow.Amount, escrow.ModelID)
		require.NotEmpty(t, escrow.AppHash, escrow.ModelID)
	}

	raw := renderServices(t, cfg)["gateway"]["DEVSHARDS_JSON"]
	var seeds []map[string]string
	require.NoError(t, json.Unmarshal([]byte(raw), &seeds), raw)
	require.Equal(t, []map[string]string{
		{"id": "1", "model": "model-a", "private_key_env": "GATEWAY_ESCROW_KEY"},
		{"id": "2", "model": "model-b", "private_key_env": "GATEWAY_ESCROW_KEY"},
	}, seeds)
}
