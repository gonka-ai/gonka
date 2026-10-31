package loadtest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// Test flow:
// 1. Write a build file with a devshardd path relative to it and load it.
// 2. Check the path resolves next to the file and the fields reach the stack config.
// 3. Drop the gateway image and check the file is refused.
func TestLoadBuildAppliesToConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build.yaml")
	body := `name: lab-gonka-pr-1792-67230e4dcc0a
protocol: v6
binary_version: lab-gonka-pr-1792-67230e4dcc0a
devshardd: bin/devshardd
images:
  versiond: lab/versiond:67230e4dcc0a
  versiond_router: lab/versiond-router:67230e4dcc0a
  gateway: lab/devshard-gateway:67230e4dcc0a
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	build, err := LoadBuild(path)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "bin", "devshardd"), build.Devshardd)

	cfg := &config.File{}
	cfg.ApplyDefaults()
	build.apply(cfg)
	require.Equal(t, "v6", cfg.Versiond.VersionName)
	require.Equal(t, "lab-gonka-pr-1792-67230e4dcc0a", cfg.Versiond.BinaryVersion)
	require.Equal(t, build.Devshardd, cfg.Versiond.HostBinaryMount)
	require.Equal(t, "lab/versiond:67230e4dcc0a", cfg.Versiond.Image)
	require.Equal(t, "lab/versiond-router:67230e4dcc0a", cfg.VersiondRouter.Image)
	require.Equal(t, "lab/devshard-gateway:67230e4dcc0a", cfg.Devshardctl.Image)
	require.True(t, cfg.MockDapi.AnnounceVersion)

	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(body, "  gateway: lab/devshard-gateway:67230e4dcc0a\n", "", 1)), 0o644))
	_, err = LoadBuild(path)
	require.ErrorContains(t, err, "images.gateway is required")
}

// Test flow:
// 1. Take this test binary as devshardd and read its build tags.
// 2. Check a scenario without in-binary faults passes either way.
// 3. Check a tripfile event and a DEVSHARD_TESTENV_ host env pass only when the binary has the testenv tag.
func TestRequireTestenvHooks(t *testing.T) {
	binary, err := os.Executable()
	require.NoError(t, err)
	tags, err := devsharddBuildTags(binary)
	require.NoError(t, err)
	tagged := slices.Contains(strings.Split(tags, ","), testenvBuildTag)

	var plain Scenario
	plain.Workload.Events = []Event{{ID: "slow", Action: "throttle", Target: "versiond-5"}}
	require.NoError(t, requireTestenvHooks(plain, binary))

	var faults Scenario
	faults.Workload.Events = []Event{{ID: "withhold", Action: tripfileEventKind, Target: "versiond-3"}}
	faults.Topology.Hosts = []TopologyHost{{ID: "versiond-3", Env: map[string]string{"DEVSHARD_TESTENV_PAYLOAD_HTTP_STATUS": "503"}}}
	err = requireTestenvHooks(faults, binary)
	if tagged {
		require.NoError(t, err)
		return
	}
	require.ErrorContains(t, err, "event withhold, versiond-3 DEVSHARD_TESTENV_PAYLOAD_HTTP_STATUS needs devshardd built with -tags devshard_testenv")
}
