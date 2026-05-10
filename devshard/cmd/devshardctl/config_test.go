package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// mapGetenv returns an os.Getenv-shaped function backed by the given
// map so tests never touch real process env (avoids cross-test
// pollution and enables parallel runs in a future change).
func mapGetenv(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

// mustParseFlags is a tiny helper that asserts no parse error. All
// call sites in this file pass concrete arg slices, so a parse error
// is a test-level bug (not a behavior to probe).
func mustParseFlags(t *testing.T, args []string) *flagSet {
	t.Helper()
	fs, err := parseFlags(args)
	require.NoError(t, err)
	return fs
}

// TestResolveConfig_FlagBeatsEnv pins the precedence contract: when
// both a flag and an env var are set, the flag wins. This is the
// behavior documented on every flag help string and the one feature
// operators rely on to override a docker-compose-supplied env var
// from a local run.
func TestResolveConfig_FlagBeatsEnv(t *testing.T) {
	fs := mustParseFlags(t, []string{
		"--private-key", "flag-key",
		"--escrow-id", "flag-esc",
	})
	env := map[string]string{
		"DEVSHARD_PRIVATE_KEY": "env-key",
		"DEVSHARD_ESCROW_ID":   "env-esc",
		"TESTENV_PRIVATE_KEY":  "testenv-key",
		"ESCROW_ID":            "testenv-esc",
	}
	cfg, err := resolveConfig(fs, mapGetenv(env))
	require.NoError(t, err)
	require.Equal(t, "flag-key", cfg.KeyHex)
	require.Equal(t, "flag-esc", cfg.EscrowID)
}

// TestResolveConfig_DevshardEnvBeatsTestenvEnv pins the env var
// precedence so a developer who exported both sees prod-named vars
// win. Guards against accidental swaps in a future refactor.
func TestResolveConfig_DevshardEnvBeatsTestenvEnv(t *testing.T) {
	fs := mustParseFlags(t, []string{})
	env := map[string]string{
		"DEVSHARD_PRIVATE_KEY": "devshard-key",
		"TESTENV_PRIVATE_KEY":  "testenv-key",
		"DEVSHARD_ESCROW_ID":   "devshard-esc",
		"ESCROW_ID":            "testenv-esc",
	}
	cfg, err := resolveConfig(fs, mapGetenv(env))
	require.NoError(t, err)
	require.Equal(t, "devshard-key", cfg.KeyHex)
	require.Equal(t, "devshard-esc", cfg.EscrowID)
}

// TestResolveConfig_TestenvEnvUsedWhenProdUnset asserts the new
// TESTENV_PRIVATE_KEY / ESCROW_ID names work when the prod env vars
// are unset. This is the Phase-9 requirement in plain form.
func TestResolveConfig_TestenvEnvUsedWhenProdUnset(t *testing.T) {
	fs := mustParseFlags(t, []string{})
	env := map[string]string{
		"TESTENV_PRIVATE_KEY": "testenv-key",
		"ESCROW_ID":           "testenv-esc",
	}
	cfg, err := resolveConfig(fs, mapGetenv(env))
	require.NoError(t, err)
	require.Equal(t, "testenv-key", cfg.KeyHex)
	require.Equal(t, "testenv-esc", cfg.EscrowID)
}

// TestResolveConfig_MissingKeyErrors asserts the resolver fails fast
// with a human-readable error that names every recognized source of
// the key so ops can fix the misconfig without grep'ing the code.
func TestResolveConfig_MissingKeyErrors(t *testing.T) {
	fs := mustParseFlags(t, []string{"--escrow-id", "esc"})
	_, err := resolveConfig(fs, mapGetenv(nil))
	require.Error(t, err)
	for _, want := range []string{
		"--private-key", "DEVSHARD_PRIVATE_KEY", "TESTENV_PRIVATE_KEY",
	} {
		require.Contains(t, err.Error(), want,
			"missing-key error must name every source: %s", want)
	}
}

// TestResolveConfig_MissingEscrowErrors asserts the escrow error
// names all three recognized sources too.
func TestResolveConfig_MissingEscrowErrors(t *testing.T) {
	fs := mustParseFlags(t, []string{"--private-key", "hex"})
	_, err := resolveConfig(fs, mapGetenv(nil))
	require.Error(t, err)
	for _, want := range []string{
		"--escrow-id", "DEVSHARD_ESCROW_ID", "ESCROW_ID",
	} {
		require.Contains(t, err.Error(), want)
	}
}

// TestResolveConfig_MockChainFlagOverridesChainRest asserts
// --mock-chain wins over --chain-rest so a developer running an
// explicit `--mock-chain mock-chain:9090` doesn't get trumped by the
// default REST URL.
func TestResolveConfig_MockChainFlagOverridesChainRest(t *testing.T) {
	fs := mustParseFlags(t, []string{
		"--private-key", "k", "--escrow-id", "e",
		"--chain-rest", "http://real-chain:1317",
		"--mock-chain", "mock-chain:9090",
	})
	cfg, err := resolveConfig(fs, mapGetenv(nil))
	require.NoError(t, err)
	require.Equal(t, "mock-chain:9090", cfg.MockChainURL)
	require.Empty(t, cfg.ChainRESTURL,
		"ChainRESTURL must be empty when MockChainURL is set, so buildBridge picks the right branch")
}

// TestResolveConfig_MockChainEnvOverridesChainRest mirrors the above
// but via MOCK_CHAIN_URL env. Useful because docker-compose typically
// drives this via env, not flags.
func TestResolveConfig_MockChainEnvOverridesChainRest(t *testing.T) {
	fs := mustParseFlags(t, []string{"--private-key", "k", "--escrow-id", "e"})
	env := map[string]string{"MOCK_CHAIN_URL": "mc:9090"}
	cfg, err := resolveConfig(fs, mapGetenv(env))
	require.NoError(t, err)
	require.Equal(t, "mc:9090", cfg.MockChainURL)
	require.Empty(t, cfg.ChainRESTURL)
}

// TestResolveConfig_ChainRESTDefaultUsedWhenMockUnset asserts the
// prod REST bridge path still works with no testenv knobs — i.e. the
// Phase 9 additions do not regress the existing prod contract.
func TestResolveConfig_ChainRESTDefaultUsedWhenMockUnset(t *testing.T) {
	fs := mustParseFlags(t, []string{"--private-key", "k", "--escrow-id", "e"})
	cfg, err := resolveConfig(fs, mapGetenv(nil))
	require.NoError(t, err)
	require.Empty(t, cfg.MockChainURL)
	require.Equal(t, defaultChainREST, cfg.ChainRESTURL)
}

// TestResolveConfig_ModelPortDefaults documents the default model
// and port when no override is supplied. Guards against accidental
// drift of the documented OpenAI-compatible port.
func TestResolveConfig_ModelPortDefaults(t *testing.T) {
	fs := mustParseFlags(t, []string{"--private-key", "k", "--escrow-id", "e"})
	cfg, err := resolveConfig(fs, mapGetenv(nil))
	require.NoError(t, err)
	require.Equal(t, defaultModel, cfg.Model)
	require.Equal(t, defaultPort, cfg.Port)
}

// TestResolveConfig_ModelPortEnvApplied asserts DEVSHARD_MODEL /
// DEVSHARD_PORT are consumed when the flag is at its default — the
// same "env-only-when-flag-is-default" semantics the original code
// used for these two fields, preserved across the Phase-9 refactor.
func TestResolveConfig_ModelPortEnvApplied(t *testing.T) {
	fs := mustParseFlags(t, []string{"--private-key", "k", "--escrow-id", "e"})
	env := map[string]string{"DEVSHARD_MODEL": "custom/model", "DEVSHARD_PORT": "12345"}
	cfg, err := resolveConfig(fs, mapGetenv(env))
	require.NoError(t, err)
	require.Equal(t, "custom/model", cfg.Model)
	require.Equal(t, "12345", cfg.Port)
}

// TestResolveConfig_StoragePathDefaultsUnderHome asserts the default
// path lives under the user cache dir and embeds the escrow id.
// Protects the on-disk layout downstream tooling relies on.
func TestResolveConfig_StoragePathDefaultsUnderHome(t *testing.T) {
	fs := mustParseFlags(t, []string{"--private-key", "k", "--escrow-id", "my-escrow-42"})
	cfg, err := resolveConfig(fs, mapGetenv(nil))
	require.NoError(t, err)
	require.True(t, strings.Contains(cfg.StoragePath, "gonka"),
		"default storage path must live under gonka cache: %s", cfg.StoragePath)
	require.True(t, strings.HasSuffix(cfg.StoragePath, "devshard-my-escrow-42.db"),
		"default storage path must embed escrow id: %s", cfg.StoragePath)
}

// TestDescribeBridge_Helpers asserts the log-line helper is
// unambiguous for which bridge is active.
func TestDescribeBridge_Helpers(t *testing.T) {
	cfgMock := resolvedConfig{MockChainURL: "mock-chain:9090"}
	require.Equal(t, "mock-chain:9090", describeBridge(cfgMock))

	cfgRest := resolvedConfig{ChainRESTURL: "http://real:1317"}
	require.Equal(t, "rest:http://real:1317", describeBridge(cfgRest))
}

// TestBuildBridge_SelectsMockChainWhenSet asserts buildBridge picks
// the testenv gRPC bridge when MockChainURL is set. We don't dial a
// real server here — grpc.NewClient is lazy.
func TestBuildBridge_SelectsMockChainWhenSet(t *testing.T) {
	cfg := resolvedConfig{MockChainURL: "mock:9090"}
	br, err := buildBridge(cfg)
	require.NoError(t, err)
	require.NotNil(t, br)
}

// TestBuildBridge_SelectsRESTWhenMockUnset asserts the REST bridge when
// mock-chain URL is empty.
func TestBuildBridge_SelectsRESTWhenMockUnset(t *testing.T) {
	cfg := resolvedConfig{ChainRESTURL: "http://x:1317"}
	br, err := buildBridge(cfg)
	require.NoError(t, err)
	require.NotNil(t, br)
}
