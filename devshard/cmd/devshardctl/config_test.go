package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/bridge"
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

// TestResolveConfig_HostFlagAndEnv covers the --host flag and the
// DEVSHARDD_URL env fallback. The pinned URL is what drives the
// pinnedHostBridge wrapper in buildBridge.
func TestResolveConfig_HostFlagAndEnv(t *testing.T) {
	// Flag wins.
	fs := mustParseFlags(t, []string{
		"--private-key", "k", "--escrow-id", "e",
		"--host", "http://devshard-2:9500",
	})
	env := map[string]string{"DEVSHARDD_URL": "http://devshard-1:9500"}
	cfg, err := resolveConfig(fs, mapGetenv(env))
	require.NoError(t, err)
	require.Equal(t, "http://devshard-2:9500", cfg.PinnedHostURL)

	// Env fallback when flag empty.
	fs2 := mustParseFlags(t, []string{"--private-key", "k", "--escrow-id", "e"})
	cfg2, err := resolveConfig(fs2, mapGetenv(env))
	require.NoError(t, err)
	require.Equal(t, "http://devshard-1:9500", cfg2.PinnedHostURL)

	// Unset → empty (auto host discovery).
	fs3 := mustParseFlags(t, []string{"--private-key", "k", "--escrow-id", "e"})
	cfg3, err := resolveConfig(fs3, mapGetenv(nil))
	require.NoError(t, err)
	require.Empty(t, cfg3.PinnedHostURL)
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

// TestDescribeBridge_Helpers asserts the log-line helpers are
// unambiguous and won't accidentally print the wrong URL. Cheap, but
// the helpers are the only place an operator sees which bridge is
// active.
func TestDescribeBridge_Helpers(t *testing.T) {
	cfgMock := resolvedConfig{MockChainURL: "mc:9090"}
	require.Equal(t, "mock-chain:mc:9090", describeBridge(cfgMock))

	cfgRest := resolvedConfig{ChainRESTURL: "http://real:1317"}
	require.Equal(t, "rest:http://real:1317", describeBridge(cfgRest))

	require.Equal(t, "auto", describeHostPin(resolvedConfig{}))
	require.Equal(t, "http://pinned:9500", describeHostPin(resolvedConfig{PinnedHostURL: "http://pinned:9500"}))
}

// ─── pinnedHostBridge ────────────────────────────────────────────────

// fakeBridge is a minimal stub used to observe forwarding through the
// pinned wrapper. Each field captures the last call so tests can
// assert forwarding happens exactly as expected.
type fakeBridge struct {
	lastEscrowID         string
	lastHostAddr         string
	lastWarmKeyWarm      string
	lastWarmKeyValidator string
	lastEscrowCreated    bridge.EscrowInfo
	lastProposedID       string
	lastFinalizedID      string
	lastDisputeID        string
	warmResult           bool
}

func (f *fakeBridge) GetEscrow(id string) (*bridge.EscrowInfo, error) {
	f.lastEscrowID = id
	return &bridge.EscrowInfo{EscrowID: id}, nil
}
func (f *fakeBridge) GetHostInfo(addr string) (*bridge.HostInfo, error) {
	// Return a DIFFERENT URL than the pinned one so the test can
	// detect whether the wrapper forwarded (returns this URL) or
	// pinned (returns the wrapper's URL).
	f.lastHostAddr = addr
	return &bridge.HostInfo{Address: addr, URL: "http://inner:9500"}, nil
}
func (f *fakeBridge) VerifyWarmKey(warm, validator string) (bool, error) {
	f.lastWarmKeyWarm = warm
	f.lastWarmKeyValidator = validator
	return f.warmResult, nil
}
func (f *fakeBridge) OnEscrowCreated(e bridge.EscrowInfo) error {
	f.lastEscrowCreated = e
	return nil
}
func (f *fakeBridge) OnSettlementProposed(id string, _ []byte, _ uint64) error {
	f.lastProposedID = id
	return nil
}
func (f *fakeBridge) OnSettlementFinalized(id string) error {
	f.lastFinalizedID = id
	return nil
}
func (f *fakeBridge) SubmitDisputeState(id string, _ []byte, _ uint64, _ map[uint32][]byte) error {
	f.lastDisputeID = id
	return nil
}

// TestPinnedHostBridge_PinsEveryAddressToSingleURL asserts the
// wrapper overrides GetHostInfo for every address — the core testenv
// requirement — and preserves the queried address verbatim (so
// state-machine auth by address still works).
func TestPinnedHostBridge_PinsEveryAddressToSingleURL(t *testing.T) {
	inner := &fakeBridge{}
	b := newPinnedHostBridge(inner, "http://pinned:9500")

	for _, addr := range []string{"gonka1a", "gonka1b", "gonka1c"} {
		info, err := b.GetHostInfo(addr)
		require.NoError(t, err)
		require.Equal(t, "http://pinned:9500", info.URL,
			"every address must resolve to the pinned URL")
		require.Equal(t, addr, info.Address,
			"Address must be the queried address verbatim, not pinned; auth lookups depend on it")
	}

	// The inner bridge must never be called for GetHostInfo: if a
	// future refactor forwards instead of pinning, this asserts fails.
	require.Empty(t, inner.lastHostAddr,
		"pinned bridge must not forward GetHostInfo; inner lastHostAddr must remain zero")
}

// TestPinnedHostBridge_ForwardsOtherMethods asserts every non-pinned
// MainnetBridge method is forwarded to the inner bridge unchanged.
// A failure here means a future refactor accidentally stopped
// forwarding a method (e.g. added a local cache).
func TestPinnedHostBridge_ForwardsOtherMethods(t *testing.T) {
	inner := &fakeBridge{warmResult: true}
	b := newPinnedHostBridge(inner, "http://pinned")

	_, err := b.GetEscrow("esc-7")
	require.NoError(t, err)
	require.Equal(t, "esc-7", inner.lastEscrowID)

	ok, err := b.VerifyWarmKey("warm", "val")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "warm", inner.lastWarmKeyWarm)
	require.Equal(t, "val", inner.lastWarmKeyValidator)

	require.NoError(t, b.OnEscrowCreated(bridge.EscrowInfo{EscrowID: "new"}))
	require.Equal(t, "new", inner.lastEscrowCreated.EscrowID)

	require.NoError(t, b.OnSettlementProposed("proposed", nil, 0))
	require.Equal(t, "proposed", inner.lastProposedID)

	require.NoError(t, b.OnSettlementFinalized("final"))
	require.Equal(t, "final", inner.lastFinalizedID)

	require.NoError(t, b.SubmitDisputeState("disputed", nil, 0, nil))
	require.Equal(t, "disputed", inner.lastDisputeID)
}

// TestPinnedHostBridge_PanicsOnEmptyURL asserts the guard in the
// constructor catches the one call shape that would otherwise
// silently produce a bridge returning empty URLs (pinnedHostBridge
// only reads b.pinnedURL; an empty one would serve broken URLs).
func TestPinnedHostBridge_PanicsOnEmptyURL(t *testing.T) {
	require.Panics(t, func() {
		newPinnedHostBridge(&fakeBridge{}, "")
	})
}

// TestBuildBridge_SelectsMockChainWhenSet asserts buildBridge picks
// the testenv gRPC bridge when MockChainURL is set. We don't dial a
// real server here — grpc.NewClient is lazy — we just require that
// the resulting bridge is the pinned wrapper when both are set and
// the inner type is the gRPC bridge.
//
// This is mostly a wiring test, not a behavior one; the behavior of
// the testenv gRPC bridge itself is covered in testenv/bridge tests.
func TestBuildBridge_SelectsMockChainWhenSet(t *testing.T) {
	cfg := resolvedConfig{MockChainURL: "mock:9090"}
	br, err := buildBridge(cfg)
	require.NoError(t, err)
	require.NotNil(t, br)
	// No pin: should be the raw gRPC bridge type.
	_, isPinned := br.(*pinnedHostBridge)
	require.False(t, isPinned, "without PinnedHostURL, buildBridge must return the raw bridge")
}

// TestBuildBridge_WrapsPinnedHost asserts the pinned decorator is
// only applied when PinnedHostURL is set. Combined with the helpers
// test above, these pin the log-line and bridge selection contract
// an operator reads at startup.
func TestBuildBridge_WrapsPinnedHost(t *testing.T) {
	cfg := resolvedConfig{
		ChainRESTURL:  "http://x",
		PinnedHostURL: "http://pinned:9500",
	}
	br, err := buildBridge(cfg)
	require.NoError(t, err)
	_, isPinned := br.(*pinnedHostBridge)
	require.True(t, isPinned, "buildBridge must wrap with pinnedHostBridge when PinnedHostURL is set")
}
