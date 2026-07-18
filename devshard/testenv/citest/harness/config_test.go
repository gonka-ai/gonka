package harness

import (
	"os"
	"path/filepath"
	"testing"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

func TestWriteS1Config_TwoHostsMultiMode(t *testing.T) {
	dir := t.TempDir()
	WriteS1Config(t, dir)

	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	require.Equal(t, config.VersiondModeMulti, cfg.Versiond.Mode)
	require.True(t, cfg.Postgres.Enabled)
	require.Len(t, cfg.Hosts, 2)
	require.Equal(t, "versiond-0", cfg.Hosts[0].ID)
	require.Equal(t, "versiond-1", cfg.Hosts[1].ID)
	// S1 omits validation_rate; ApplyDefaults → 6000.
	cfg.ApplyDefaults()
	require.Equal(t, uint32(6000), cfg.Params.ValidationRate)
}

func TestWriteS9Config_ValidationRate100(t *testing.T) {
	dir := t.TempDir()
	WriteS9Config(t, dir)

	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	require.Len(t, cfg.Hosts, 3)
	require.Equal(t, uint32(10000), cfg.Params.ValidationRate)
	require.Equal(t, uint32(10000), cfg.Escrows[0].ValidationRate)
}

func TestWriteMultiConfig_CustomValidationRate(t *testing.T) {
	dir := t.TempDir()
	WriteMultiConfig(t, dir, MultiConfigOpts{Hosts: 2, EscrowSlots: 2, ValidationRate: 7500})

	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	require.Equal(t, uint32(7500), cfg.Params.ValidationRate)
	require.Equal(t, uint32(7500), cfg.Escrows[0].ValidationRate)
}

func TestWriteS1Config_GencomposeProducesTwoVersiondServices(t *testing.T) {
	if os.Getenv("TESTENV_HARNESS_GENCOMPOSE") != "1" {
		t.Skip("set TESTENV_HARNESS_GENCOMPOSE=1 to run gencompose harness test")
	}
	RequireDocker(t)

	stack := NewStack(t, "citest-harness-gen-*")
	WriteS1Config(t, stack.WorkDir)
	stack.RunGencompose(t)

	cfg := stack.LoadConfig(t)
	require.Len(t, cfg.Hosts, 2)

	body, err := os.ReadFile(stack.ComposePath)
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "versiond-0:")
	require.Contains(t, text, "versiond-1:")
	require.NotContains(t, text, "versiond-2:")
	require.Contains(t, text, `"127.0.0.1::8080"`)
}
