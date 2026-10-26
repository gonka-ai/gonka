package upgrade

import (
	"context"
	"decentralized-api/apiconfig"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWriteUpgradeInfoUsesHeightBeforeHalt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "upgrade-info.json")
	plan := apiconfig.UpgradePlan{
		Name:   "v0.0.1test",
		Height: 263,
		Binaries: map[string]string{
			"linux/amd64": "http://genesis-mock-server:8080/files/dapi.zip",
		},
	}

	require.NoError(t, writeUpgradeInfo(path, plan))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var output UpgradeOutput
	require.NoError(t, json.Unmarshal(raw, &output))
	require.Equal(t, "v0.0.1test", output.Name)
	require.Equal(t, int64(262), output.Height)

	var info UpgradeInfoOutput
	require.NoError(t, json.Unmarshal([]byte(output.Info), &info))
	require.Equal(t, plan.Binaries, info.Binaries)
}

func TestCheckForUpgradeWritesFileBeforeClearingPlan(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`api:
  port: 8080
chain_node:
  url: http://join1-node:26657
  signer_key_name: join1
  keyring_backend: test
  keyring_dir: /root/.inference
current_height: 1
merged_node_config: true
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "node-config.json"), []byte("[]"), 0o644))
	mgr, err := apiconfig.LoadConfigManagerWithPaths(
		cfgPath,
		filepath.Join(dir, "gonka.db"),
		filepath.Join(dir, "node-config.json"),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		if db := mgr.SqlDb().GetDb(); db != nil {
			_ = db.Close()
		}
		consumePendingUpgrade()
	})

	require.NoError(t, mgr.SetHeight(262))
	plan := apiconfig.UpgradePlan{
		Name:     "v0.0.1test",
		Height:   263,
		Binaries: map[string]string{"linux/amd64": "http://example/dapi.zip"},
	}
	require.NoError(t, mgr.SetUpgradePlan(plan))

	infoPath := filepath.Join(dir, "upgrade-info.json")
	oldPath := upgradeInfoPath
	upgradeInfoPath = infoPath
	t.Cleanup(func() { upgradeInfoPath = oldPath })

	require.True(t, CheckForUpgrade(mgr))
	raw, err := os.ReadFile(infoPath)
	require.NoError(t, err)
	var output UpgradeOutput
	require.NoError(t, json.Unmarshal(raw, &output))
	require.Equal(t, int64(262), output.Height)
	require.Empty(t, mgr.GetUpgradePlan().Name)

	require.NoError(t, mgr.FlushNow(context.Background()))
	if db := mgr.SqlDb().GetDb(); db != nil {
		require.NoError(t, db.Close())
	}

	stopWait := make(chan struct{})
	pendingUpgradeMu.Lock()
	upgradeWaitDone = stopWait
	pendingUpgradeMu.Unlock()
	t.Cleanup(func() {
		close(stopWait)
		pendingUpgradeMu.Lock()
		upgradeWaitDone = nil
		pendingUpgradeMu.Unlock()
	})

	done := make(chan struct{})
	go func() {
		FinishUpgradeForCosmovisor()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("FinishUpgradeForCosmovisor returned before cosmovisor signal")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestCheckForUpgradeKeepsPlanWhenWriteFails(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`api:
  port: 8080
chain_node:
  url: http://join1-node:26657
  signer_key_name: join1
  keyring_backend: test
  keyring_dir: /root/.inference
current_height: 1
merged_node_config: true
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "node-config.json"), []byte("[]"), 0o644))
	mgr, err := apiconfig.LoadConfigManagerWithPaths(
		cfgPath,
		filepath.Join(dir, "gonka.db"),
		filepath.Join(dir, "node-config.json"),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		if db := mgr.SqlDb().GetDb(); db != nil {
			_ = db.Close()
		}
		consumePendingUpgrade()
	})

	require.NoError(t, mgr.SetHeight(262))
	plan := apiconfig.UpgradePlan{
		Name:     "v0.0.1test",
		Height:   263,
		Binaries: map[string]string{"linux/amd64": "http://example/dapi.zip"},
	}
	require.NoError(t, mgr.SetUpgradePlan(plan))

	blocker := filepath.Join(dir, "not-a-directory")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	oldPath := upgradeInfoPath
	upgradeInfoPath = filepath.Join(blocker, "upgrade-info.json")
	t.Cleanup(func() { upgradeInfoPath = oldPath })

	require.False(t, CheckForUpgrade(mgr))
	require.Equal(t, plan.Name, mgr.GetUpgradePlan().Name)

	done := make(chan struct{})
	go func() {
		FinishUpgradeForCosmovisor()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("FinishUpgradeForCosmovisor waited without a published upgrade file")
	}
}
