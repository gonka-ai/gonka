package main

import (
	"os"
	"path/filepath"
	"testing"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// Test flow:
// 1. Fill a default config with two ML nodes and bind the second to versiond-2.
// 2. Write the compose file.
// 3. Require mock-dapi to get the node-to-host binding and every host's IP mapping.
func TestWriteCompose_MockDapiHostBinding(t *testing.T) {
	cfg := defaultConfig()
	require.NoError(t, fillConfig(cfg))
	cfg.MockOpenAI.Nodes = []config.MockOpenAINodeCfg{{Name: "mock-openai-0"}, {Name: "mock-openai-1", Host: "versiond-2"}}

	outPath := filepath.Join(t.TempDir(), "docker-compose.yml")
	require.NoError(t, writeCompose(cfg, outPath))
	body, err := os.ReadFile(outPath)
	require.NoError(t, err)
	text := string(body)

	require.Contains(t, text, `MOCK_ML_NODE_HOSTS: "mock-openai-1=versiond-2"`)
	require.Equal(t, "mock-openai-1=versiond-2", mockMLNodeHostsEnv(cfg))
	addrs := mockDapiHostAddrsEnv(cfg)
	require.Contains(t, text, `MOCK_DAPI_HOST_ADDRS: "`+addrs+`"`)
	for _, host := range cfg.Hosts {
		require.Contains(t, addrs, host.ID+"="+host.ID)
		require.NotContains(t, addrs, host.IP)
	}
}

// Test flow:
// 1. Fill a default config, then blank every host IP as the load runner does.
// 2. Require MOCK_DAPI_HOST_ADDRS to still map every host by its service name.
func TestMockDapiHostAddrsEnv_WithoutStaticIPs(t *testing.T) {
	cfg := defaultConfig()
	require.NoError(t, fillConfig(cfg))
	for i := range cfg.Hosts {
		cfg.Hosts[i].IP = ""
	}
	addrs := mockDapiHostAddrsEnv(cfg)
	require.NotEmpty(t, cfg.Hosts)
	for _, host := range cfg.Hosts {
		require.Contains(t, addrs, host.ID+"="+host.ID)
	}
}
