package apiconfig_test

import (
	"context"
	"decentralized-api/apiconfig"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeTempFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(contents), 0644))
	return p
}

// Scenario 1: YAML has dynamic state, DB empty, node-config.json present but should be ignored because merged_node_config=true
func TestLoadDefaultConfigManager_Migration_Idempotent_And_NodeConfigSkipped(t *testing.T) {
	tmp := t.TempDir()

	yaml := `api:
  port: 8080
chain_node:
  url: http://join1-node:26657
  signer_key_name: join1
  account_public_key: ""
  keyring_backend: test
  keyring_dir: /root/.inference
current_height: 393
current_seed:
  seed: 3898730504561900192
  epoch_index: 380
  signature: abc
previous_seed:
  seed: 1370553182438852893
  epoch_index: 370
  signature: def
upcoming_seed:
  seed: 254929314898674592
  epoch_index: 390
  signature: ghi
nodes:
  - host: http://yaml-node:8080/
    models:
      modelA: {args: []}
    id: yaml-node-1
    max_concurrent: 5
merged_node_config: true
current_node_version: "v3.0.8"
`
	cfgPath := writeTempFile(t, tmp, "config.yaml", yaml)

	// Present but must be ignored due to merged_node_config=true
	nodeJson := `[{"host":"http://json-node:8080/","inference_port":8080,"poc_port":5000,"models":{"modelB":{"args":[]}},"id":"json-node-1","max_concurrent":10,"hardware":[]}]`
	nodePath := writeTempFile(t, tmp, "node-config.json", nodeJson)
	dbPath := filepath.Join(tmp, "test.db")
	_ = os.Remove(dbPath)

	// First load -> migration and hydrate
	mgr, err := apiconfig.LoadConfigManagerWithPaths(cfgPath, dbPath, nodePath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, mgr.FlushNow(ctx))

	// Migration flag present
	var dummy bool
	ok, err := apiconfig.KVGetJSON(ctx, mgr.SqlDb().GetDb(), "config_migrated", &dummy)
	require.NoError(t, err)
	require.True(t, ok)

	// Nodes come from YAML, not JSON
	nodes, err := apiconfig.ReadNodes(ctx, mgr.SqlDb().GetDb())
	require.NoError(t, err)
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.Id)
	}
	b, _ := json.Marshal(ids)
	require.NotContains(t, ids, "json-node-1", string(b))
	require.Contains(t, ids, "yaml-node-1", string(b))

	// Second load -> idempotent
	mgr2, err := apiconfig.LoadConfigManagerWithPaths(cfgPath, dbPath, nodePath)
	require.NoError(t, err)
	require.NoError(t, mgr2.FlushNow(ctx))
	nodes2, err := apiconfig.ReadNodes(ctx, mgr2.SqlDb().GetDb())
	require.NoError(t, err)
	ids2 := make([]string, 0, len(nodes2))
	for _, n := range nodes2 {
		ids2 = append(ids2, n.Id)
	}
	require.Equal(t, ids, ids2)

	// Cleanup
	_ = os.Remove(dbPath)
}

// Scenario 2: Migration + node-config merge on first run (merged_node_config=false)
func TestMigrationAndNodeConfigMerge_FirstRun(t *testing.T) {
	tmp := t.TempDir()

	yaml := `api:
  port: 8080
current_height: 5
nodes:
  - host: http://yaml-node:8080/
    models:
      modelY: {args: []}
    id: yaml-node
    max_concurrent: 3
merged_node_config: false
`
	cfgPath := writeTempFile(t, tmp, "config.yaml", yaml)

	nodeJson := `[{"host":"http://json-node:8080/","inference_port":8080,"poc_port":5000,"models":{"modelZ":{"args":[]}},"id":"json-node","max_concurrent":2,"hardware":[]}]`
	nodePath := writeTempFile(t, tmp, "node-config.json", nodeJson)
	dbPath := filepath.Join(tmp, "test.db")
	_ = os.Remove(dbPath)

	mgr, err := apiconfig.LoadConfigManagerWithPaths(cfgPath, dbPath, nodePath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, mgr.FlushNow(ctx))

	// KV flags set
	var dummy bool
	ok, err := apiconfig.KVGetJSON(ctx, mgr.SqlDb().GetDb(), "config_migrated", &dummy)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = apiconfig.KVGetJSON(ctx, mgr.SqlDb().GetDb(), "node_config_merged", &dummy)
	require.NoError(t, err)
	require.True(t, ok)

	nodes, err := apiconfig.ReadNodes(ctx, mgr.SqlDb().GetDb())
	require.NoError(t, err)
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.Id)
	}
	b, _ := json.Marshal(ids)
	// Because merged_node_config was false, JSON must win on first run
	require.Contains(t, ids, "json-node", string(b))
}

// Scenario 3: First run without node-config.json (merged_node_config=false)
func TestFirstRun_NoNodeConfig_UsesYamlNodesAndStripsDynamicFromYaml(t *testing.T) {
	tmp := t.TempDir()
	yaml := `api:
  port: 8080
current_height: 7
nodes:
  - host: http://yaml-node:8080/
    models:
      modelY: {args: []}
    id: yaml-node
    max_concurrent: 3
merged_node_config: false
`
	cfgPath := writeTempFile(t, tmp, "config.yaml", yaml)
	// No node-config.json
	dbPath := filepath.Join(tmp, "test.db")
	_ = os.Remove(dbPath)

	mgr, err := apiconfig.LoadConfigManagerWithPaths(cfgPath, dbPath, "")
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, mgr.FlushNow(ctx))

	// DB has yaml nodes
	nodes, err := apiconfig.ReadNodes(ctx, mgr.SqlDb().GetDb())
	require.NoError(t, err)
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.Id)
	}
	require.Contains(t, ids, "yaml-node")

	// After Write(), static-only YAML should be persisted; dynamic fields zeroed (e.g., current_height: 0)
	// Call Write() explicitly and re-read file
	require.NoError(t, mgr.Write())
	data, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	s := string(data)
	require.Contains(t, s, "current_height: 0")

	// Now reload manager and ensure zero in YAML is ignored and DB value is used (should be 7)
	mgr2, err := apiconfig.LoadConfigManagerWithPaths(cfgPath, dbPath, "")
	require.NoError(t, err)
	require.Equal(t, int64(7), mgr2.GetHeight())
}

// Scenario 4: Relaunch after migration; ignore dynamic YAML and skip migration
func TestRelaunchAfterMigration_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "test.db")
	yaml1 := `api:
  port: 8080
current_height: 10
nodes:
  - host: http://yaml-node:8080/
    models:
      modelY: {args: []}
    id: yaml-node
    max_concurrent: 3
merged_node_config: false
`
	cfgPath := writeTempFile(t, tmp, "config.yaml", yaml1)

	// First run with JSON nodes imported
	nodeJson := `[{"host":"http://json-node:8080/","inference_port":8080,"poc_port":5000,"models":{"modelZ":{"args":[]}},"id":"json-node","max_concurrent":2,"hardware":[]}]`
	nodePath := writeTempFile(t, tmp, "node-config.json", nodeJson)
	_ = os.Remove(dbPath)
	mgr, err := apiconfig.LoadConfigManagerWithPaths(cfgPath, dbPath, nodePath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, mgr.FlushNow(ctx))

	// Second run with conflicting YAML dynamic (try to re-introduce nonsense)
	yaml2 := `api:
  port: 8080
current_height: 999
merged_node_config: true
nodes:
  - host: http://yaml-node2:8080/
    models:
      modelY: {args: []}
    id: yaml-node2
    max_concurrent: 3
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml2), 0644))
	mgr2, err := apiconfig.LoadConfigManagerWithPaths(cfgPath, dbPath, nodePath)
	require.NoError(t, err)
	require.NoError(t, mgr2.FlushNow(ctx))

	nodes, err := apiconfig.ReadNodes(ctx, mgr2.SqlDb().GetDb())
	require.NoError(t, err)
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.Id)
	}
	b, _ := json.Marshal(ids)
	// Must still be json-node from DB, not yaml-node2
	require.Contains(t, ids, "json-node", string(b))
	require.NotContains(t, ids, "yaml-node2", string(b))
}
