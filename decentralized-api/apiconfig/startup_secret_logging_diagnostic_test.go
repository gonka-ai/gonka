package apiconfig_test

import (
	"bytes"
	"context"
	"decentralized-api/apiconfig"
	"encoding/base64"
	"encoding/json"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStartupDoesNotLogKeyringPassword(t *testing.T) {
	// Isolate configuration inputs without changing unrelated environment variables.
	keys := []string{"KEY_NAME", "ACCOUNT_PUBKEY", "KEYRING_BACKEND", "KEYRING_PASSWORD", "NODE_CONFIG_PATH"}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "DAPI_") {
			keys = append(keys, key)
		}
	}
	for _, key := range keys {
		t.Setenv(key, "")
		require.NoError(t, os.Unsetenv(key))
	}
	const password = "DUMMY_PASSWORD_MARKER_ONLY"
	original := slog.Default()
	oldWriter, oldFlags, oldPrefix := log.Writer(), log.Flags(), log.Prefix()
	defer func() {
		slog.SetDefault(original)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
	}()
	for _, format := range []string{"base", "text", "json"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			if format == "base" {
				slog.SetDefault(original)
				log.SetOutput(&output)
			} else if format == "text" {
				slog.SetDefault(slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelInfo})))
			} else {
				slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelInfo})))
			}
			log.SetFlags(0)
			log.SetPrefix("")
			t.Setenv("KEYRING_PASSWORD", password)
			dir := t.TempDir()
			cfgPath, dbPath := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "fixture.db")
			fixture := "api:\n  port: 8080\nchain_node:\n  keyring_backend: test\nmerged_node_config: true\n"
			if err := os.WriteFile(cfgPath, []byte(fixture), 0600); err != nil {
				t.Fatal("write dummy config")
			}
			manager, err := apiconfig.LoadConfigManagerWithPaths(cfgPath, dbPath, "")
			if err != nil {
				t.Fatal("load dummy startup fixture failed")
			}
			defer manager.SqlDb().GetDb().Close()
			got := output.String()
			require.Equal(t, password, manager.GetChainNodeConfig().KeyringPassword)
			if strings.Contains(got, password) {
				t.Error("startup log contains the keyring password")
			}
			require.Contains(t, got, "Final loaded config (JSON):")
			require.Contains(t, got, "Loaded KEYRING_PASSWORD")
			require.NotContains(t, got, "KEYRING_PASSWORD environment variable not set")

			// An absent password must still produce the operational warning.
			output.Reset()
			if err := os.Unsetenv("KEYRING_PASSWORD"); err != nil {
				t.Fatal("unset dummy password")
			}
			emptyDir := t.TempDir()
			emptyPath := filepath.Join(emptyDir, "config.yaml")
			if err := os.WriteFile(emptyPath, []byte("api:\n  port: 8080\nmerged_node_config: true\n"), 0600); err != nil {
				t.Fatal("write empty fixture")
			}
			empty, err := apiconfig.LoadConfigManagerWithPaths(emptyPath, filepath.Join(emptyDir, "empty.db"), "")
			if err != nil {
				t.Fatal("load empty fixture failed")
			}
			defer empty.SqlDb().GetDb().Close()
			absent := output.String()
			if !strings.Contains(absent, "KEYRING_PASSWORD environment variable not set") || strings.Contains(absent, "Loaded KEYRING_PASSWORD") {
				t.Fatal("absent password control failed")
			}
			require.Empty(t, empty.GetChainNodeConfig().KeyringPassword)
		})
	}
}

func TestExportAllDbExcludesBLSDealerOpenings(t *testing.T) {
	ctx := context.Background()
	db, err := apiconfig.OpenSQLite(apiconfig.SqliteConfig{Path: filepath.Join(t.TempDir(), "export.db")})
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, apiconfig.EnsureSchema(ctx, db))

	openings := []apiconfig.BLSDealerOpening{
		{EpochID: 1, RecipientIndex: 2, CiphertextIndex: 3, SlotIndex: 4, ShareBytes: []byte("dummy-secret-share-one"), Seed: []byte("dummy-secret-seed-one")},
		{EpochID: 5, RecipientIndex: 6, CiphertextIndex: 7, SlotIndex: 8, ShareBytes: []byte("dummy-secret-share-two"), Seed: []byte("dummy-secret-seed-two")},
	}
	require.NoError(t, apiconfig.UpsertBLSDealerOpenings(ctx, db, openings))
	stored, err := apiconfig.ReadBLSDealerOpenings(ctx, db)
	require.NoError(t, err)
	require.ElementsMatch(t, openings, stored)

	require.NoError(t, apiconfig.SetBridgeLatestBlock(ctx, db, "export-test-chain", 123))
	config := map[string]any{"enabled": true, "label": "public-config"}
	require.NoError(t, apiconfig.KVSetJSON(ctx, db, "export-test-config", config))
	// PoC seed metadata is separate from the private BLS dealer seeds.
	require.NoError(t, apiconfig.SetActiveSeed(ctx, db, "current", apiconfig.SeedInfo{
		Seed: 1234, EpochIndex: 1, Signature: "public-seed-signature",
	}))

	exported, err := apiconfig.ExportAllDb(ctx, db)
	require.NoError(t, err)
	if _, found := exported["bls_dealer_openings"]; found {
		t.Error("database export includes the secret BLS dealer openings table")
	}
	payload, err := json.Marshal(exported)
	require.NoError(t, err)
	for _, field := range []string{"bls_dealer_openings", "share_bytes"} {
		if bytes.Contains(payload, []byte(`"`+field+`"`)) {
			t.Errorf("database export contains secret field %q", field)
		}
	}
	for _, opening := range openings {
		for _, secret := range [][]byte{opening.ShareBytes, opening.Seed} {
			if bytes.Contains(payload, secret) || bytes.Contains(payload, []byte(base64.StdEncoding.EncodeToString(secret))) {
				t.Error("database export contains secret BLS dealer material")
			}
		}
	}

	for _, table := range []string{"inference_nodes", "kv_config", "seed_info", "bridge_state"} {
		require.Contains(t, exported, table)
	}
	bridgeRows, ok := exported["bridge_state"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, bridgeRows, 1)
	require.Equal(t, "export-test-chain", bridgeRows[0]["chain_id"])
	require.Equal(t, int64(123), bridgeRows[0]["latest_block"])
	configRows, ok := exported["kv_config"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, configRows, 1)
	require.Equal(t, "export-test-config", configRows[0]["key"])
	require.Equal(t, config, configRows[0]["value_json"])
	seedRows, ok := exported["seed_info"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, seedRows, 1)
	require.Equal(t, int64(1234), seedRows[0]["seed"])
	require.Equal(t, "public-seed-signature", seedRows[0]["signature"])

	stored, err = apiconfig.ReadBLSDealerOpenings(ctx, db)
	require.NoError(t, err)
	require.ElementsMatch(t, openings, stored)
}
