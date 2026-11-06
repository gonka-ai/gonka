package main

import (
	"encoding/json"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"testing"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// Test flow:
// 1. Fill a default config that carries two escrows, one per model, as the load runner writes for a model mix.
// 2. Check both escrows get the chain fields: creator, slots, amount and app hash.
// 3. Render compose and check devshardctl gets DEVSHARDS_JSON with one runtime per escrow, key by env name only.
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

	raw := renderServices(t, cfg)["devshardctl"]["DEVSHARDS_JSON"]
	var runtimes []map[string]string
	require.NoError(t, json.Unmarshal([]byte(raw), &runtimes), raw)
	require.Equal(t, []map[string]string{
		{"id": "1", "model": "model-a", "private_key_env": "DEVSHARD_PRIVATE_KEY", "storage_path": "/var/lib/devshardctl/escrow-1"},
		{"id": "2", "model": "model-b", "private_key_env": "DEVSHARD_PRIVATE_KEY", "storage_path": "/var/lib/devshardctl/escrow-2"},
	}, runtimes)
}

// Test flow:
// 1. Fill and render the default single-escrow config.
// 2. Check devshardctl has no DEVSHARDS_JSON, so it keeps the single-escrow env path.
func TestWriteCompose_SingleEscrowHasNoDevshardsJSON(t *testing.T) {
	cfg := defaultConfig()
	require.NoError(t, fillConfig(cfg))
	_, set := renderServices(t, cfg)["devshardctl"]["DEVSHARDS_JSON"]
	require.False(t, set)
}

// Test flow: the standalone gateway needs DEVSHARDS_JSON even for one escrow.
// Its generated service must execute the new binary and use the supplied image.
func TestWriteCompose_StandaloneGatewaySingleEscrow(t *testing.T) {
	cfg := defaultConfig()
	cfg.Devshardctl.Binary = "gateway"
	cfg.Devshardctl.Image = "local/gateway:46ce696a-v2"
	require.NoError(t, fillConfig(cfg))
	env := renderServices(t, cfg)["devshardctl"]
	require.Equal(t, "true", env["DEVSHARD_STATS_ENABLED"])
	var seeds []map[string]string
	require.NoError(t, json.Unmarshal([]byte(env["DEVSHARDS_JSON"]), &seeds))
	require.Len(t, seeds, 1)
	require.Equal(t, "DEVSHARD_PRIVATE_KEY", seeds[0]["private_key_env"])
	out := filepath.Join(t.TempDir(), "compose.yaml")
	require.NoError(t, writeCompose(cfg, out))
	body, err := os.ReadFile(out)
	require.NoError(t, err)
	var compose struct {
		Services map[string]struct {
			Entrypoint []string `yaml:"entrypoint"`
			Image      string   `yaml:"image"`
			PullPolicy string   `yaml:"pull_policy"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(body, &compose))
	require.Equal(t, []string{"gateway"}, compose.Services["devshardctl"].Entrypoint)
	require.Equal(t, cfg.Devshardctl.Image, compose.Services["devshardctl"].Image)
	require.Equal(t, "never", compose.Services["devshardctl"].PullPolicy)
}
