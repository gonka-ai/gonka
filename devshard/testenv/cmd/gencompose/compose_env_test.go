package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files")

func perHostEnvConfig(t *testing.T) *config.File {
	t.Helper()
	cfg := defaultConfig()
	cfg.Hosts[2].Env = map[string]string{
		"DEVSHARD_RPC_MSGS_PER_MIN":            "3000",
		"DEVSHARD_TESTENV_EXECUTOR_FAULT_DIR":  "/var/run/faults",
		"DEVSHARD_TESTENV_ORACLE_HEIGHT_DELTA": "-2",
	}
	cfg.MockOpenAI.Nodes = []config.MockOpenAINodeCfg{
		{Name: "mock-openai-0", Host: "versiond-0"},
		{Name: "mock-openai-1", Host: "versiond-2"},
		{Name: "mock-openai-2"},
	}
	require.NoError(t, fillConfig(cfg))
	return cfg
}

func renderServices(t *testing.T, cfg *config.File) map[string]map[string]string {
	t.Helper()
	outPath := filepath.Join(t.TempDir(), "docker-compose.yml")
	require.NoError(t, writeCompose(cfg, outPath))
	body, err := os.ReadFile(outPath)
	require.NoError(t, err)
	var compose struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(body, &compose))
	envs := map[string]map[string]string{}
	for name, service := range compose.Services {
		envs[name] = service.Environment
	}
	return envs
}

// Test flow:
// 1. Give versiond-2 an env map with one overridable key and two new keys, and bind two ML nodes to hosts.
// 2. Render compose and extract versiond-2's environment plus the mock-dapi binding variables.
// 3. Compare them with the golden file, and check versiond-0's defaults stay untouched.
func TestWriteCompose_PerHostEnvGolden(t *testing.T) {
	envs := renderServices(t, perHostEnvConfig(t))
	snapshot := map[string]any{
		"versiond-2": envs["versiond-2"],
		"mock-dapi": map[string]string{
			"MOCK_ML_NODE_HOSTS":   envs["mock-dapi"]["MOCK_ML_NODE_HOSTS"],
			"MOCK_DAPI_HOST_ADDRS": envs["mock-dapi"]["MOCK_DAPI_HOST_ADDRS"],
		},
	}
	got, err := yaml.Marshal(snapshot)
	require.NoError(t, err)
	golden := filepath.Join("testdata", "per_host_env.golden")
	if *updateGolden {
		require.NoError(t, os.WriteFile(golden, got, 0o644))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
	require.Equal(t, "${DEVSHARD_RPC_MSGS_PER_MIN:-}", envs["versiond-0"]["DEVSHARD_RPC_MSGS_PER_MIN"])
	require.NotContains(t, envs["versiond-0"], "DEVSHARD_TESTENV_EXECUTOR_FAULT_DIR")
}

// Test flow:
// 1. Give versiond-2 an env key that the template already sets as a fixed value.
// 2. Render compose and expect a duplicate-key error instead of a silently shadowed value.
func TestWriteCompose_PerHostEnvRejectsFixedKey(t *testing.T) {
	cfg := perHostEnvConfig(t)
	cfg.Hosts[2].Env = map[string]string{"KEY_NAME": "versiond-0"}
	err := writeCompose(cfg, filepath.Join(t.TempDir(), "docker-compose.yml"))
	require.ErrorContains(t, err, "generated compose is invalid")
}

// Test flow:
// 1. Give solo versiond-2 DEVSHARD_STORAGE_MODE=sqlite in its env map.
// 2. Render compose and expect sqlite on versiond-2 with the defaults kept on the HA pair.
func TestWriteCompose_PerHostStorageMode(t *testing.T) {
	cfg := perHostEnvConfig(t)
	cfg.Hosts[2].Env = map[string]string{"DEVSHARD_STORAGE_MODE": "sqlite"}
	envs := renderServices(t, cfg)
	require.Equal(t, "sqlite", envs["versiond-2"]["DEVSHARD_STORAGE_MODE"])
	require.Equal(t, "postgres", envs["versiond-0"]["DEVSHARD_STORAGE_MODE"])
	require.Equal(t, "postgres", envs["versiond-1"]["DEVSHARD_STORAGE_MODE"])
}

// Test flow:
// 1. Render compose from the defaults and check versiond, versiond-router and devshardctl build from the checkout.
// 2. Name a prebuilt image for each and render again.
// 3. Check each of the three runs its image and has no build section, while the mocks still build.
// 4. Check mock-dapi names no version by default and the hosts' version when asked, which the router catalog and versiond follow.
func TestWriteCompose_PrebuiltImages(t *testing.T) {
	type service struct {
		Image string         `yaml:"image"`
		Build map[string]any `yaml:"build"`
	}
	render := func(cfg *config.File) map[string]service {
		outPath := filepath.Join(t.TempDir(), "docker-compose.yml")
		require.NoError(t, writeCompose(cfg, outPath))
		body, err := os.ReadFile(outPath)
		require.NoError(t, err)
		var compose struct {
			Services map[string]service `yaml:"services"`
		}
		require.NoError(t, yaml.Unmarshal(body, &compose))
		return compose.Services
	}
	cfg := perHostEnvConfig(t)
	services := render(cfg)
	require.NotContains(t, renderServices(t, cfg)["mock-dapi"], "MOCK_DAPI_VERSION_NAME")
	require.Equal(t, "devshard-versiond:latest", services["versiond-0"].Image)
	require.NotEmpty(t, services["versiond-0"].Build)
	require.NotEmpty(t, services["versiond-router"].Build)
	require.NotEmpty(t, services["devshardctl"].Build)

	cfg.Versiond.Image = "lab/versiond:67230e4dcc0a"
	cfg.VersiondRouter.Image = "lab/versiond-router:67230e4dcc0a"
	cfg.Devshardctl.Image = "lab/devshard-gateway:67230e4dcc0a"
	services = render(cfg)
	for _, host := range cfg.Hosts {
		require.Equal(t, "lab/versiond:67230e4dcc0a", services[host.ID].Image)
		require.Empty(t, services[host.ID].Build)
	}
	require.Equal(t, "lab/versiond-router:67230e4dcc0a", services["versiond-router"].Image)
	require.Empty(t, services["versiond-router"].Build)
	require.Equal(t, "lab/devshard-gateway:67230e4dcc0a", services["devshardctl"].Image)
	require.Empty(t, services["devshardctl"].Build)
	require.NotEmpty(t, services["mock-chain"].Build)

	cfg.Versiond.VersionName = "v6"
	cfg.MockDapi.AnnounceVersion = true
	require.Equal(t, "v6", renderServices(t, cfg)["mock-dapi"]["MOCK_DAPI_VERSION_NAME"])
}
