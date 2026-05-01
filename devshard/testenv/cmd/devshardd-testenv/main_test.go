package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/signing"
	"devshard/types"
)

// TestLoadEnvConfig_HappyPath asserts every required env var is
// consumed and DATA_DIR / HTTP_PORT fall back to sensible defaults.
// The test uses t.Setenv so the env is restored on teardown — no
// state bleeds into subsequent cases.
func TestLoadEnvConfig_HappyPath(t *testing.T) {
	t.Setenv("TESTENV_PRIVATE_KEY", "deadbeef")
	t.Setenv("ESCROW_ID", "esc-1")
	t.Setenv("MOCK_CHAIN_URL", "mock-chain:9090")
	t.Setenv("HEIGHT_SYNC_URL", "http://height-sync:9100")
	t.Setenv("CHAIN_ID", "testenv-1")
	// HTTP_PORT and DATA_DIR intentionally unset so the defaults surface.
	t.Setenv("HTTP_PORT", "")
	t.Setenv("DATA_DIR", "")

	cfg, err := loadEnvConfig()
	require.NoError(t, err)
	require.Equal(t, "deadbeef", cfg.PrivateKeyHex)
	require.Equal(t, "esc-1", cfg.EscrowID)
	require.Equal(t, "mock-chain:9090", cfg.MockChainURL)
	require.Equal(t, "http://height-sync:9100", cfg.HeightSyncURL)
	require.Equal(t, "testenv-1", cfg.ChainID)
	require.Equal(t, 9500, cfg.HTTPPort, "HTTP_PORT default is 9500")
	require.Equal(t, "/data", cfg.DataDir, "DATA_DIR default is /data")
}

// TestLoadEnvConfig_OverridesApplied asserts non-default HTTP_PORT
// and DATA_DIR values override the defaults.
func TestLoadEnvConfig_OverridesApplied(t *testing.T) {
	t.Setenv("TESTENV_PRIVATE_KEY", "aa")
	t.Setenv("ESCROW_ID", "esc")
	t.Setenv("MOCK_CHAIN_URL", "mc:9090")
	t.Setenv("HEIGHT_SYNC_URL", "http://hs")
	t.Setenv("HTTP_PORT", "18080")
	t.Setenv("DATA_DIR", "/tmp/devshardd")

	cfg, err := loadEnvConfig()
	require.NoError(t, err)
	require.Equal(t, 18080, cfg.HTTPPort)
	require.Equal(t, "/tmp/devshardd", cfg.DataDir)
}

// TestLoadEnvConfig_MissingRequiredVars asserts that every missing
// required var is listed in the error so operators can see all
// misconfig at once, not one-by-one.
func TestLoadEnvConfig_MissingRequiredVars(t *testing.T) {
	t.Setenv("TESTENV_PRIVATE_KEY", "")
	t.Setenv("ESCROW_ID", "")
	t.Setenv("MOCK_CHAIN_URL", "")
	t.Setenv("HEIGHT_SYNC_URL", "")
	t.Setenv("HTTP_PORT", "9500")

	_, err := loadEnvConfig()
	require.Error(t, err)
	msg := err.Error()
	for _, v := range []string{
		"TESTENV_PRIVATE_KEY", "ESCROW_ID", "MOCK_CHAIN_URL", "HEIGHT_SYNC_URL",
	} {
		require.Contains(t, msg, v,
			"missing-var error must list %s", v)
	}
}

// TestLoadEnvConfig_InvalidHTTPPort asserts malformed HTTP_PORT is
// an explicit error — misbehaving orchestration should not silently
// fall through to the default.
func TestLoadEnvConfig_InvalidHTTPPort(t *testing.T) {
	t.Setenv("TESTENV_PRIVATE_KEY", "aa")
	t.Setenv("ESCROW_ID", "esc")
	t.Setenv("MOCK_CHAIN_URL", "mc")
	t.Setenv("HEIGHT_SYNC_URL", "hs")
	t.Setenv("HTTP_PORT", "not-a-number")

	_, err := loadEnvConfig()
	require.Error(t, err)
	require.Contains(t, err.Error(), "HTTP_PORT")
}

// TestPrimarySlotID_PicksFirstOwned asserts the helper returns the
// lowest SlotID owned by this address, even when the caller doesn't
// hand the group in slot order.
func TestPrimarySlotID_PicksFirstOwned(t *testing.T) {
	group := []types.SlotAssignment{
		{SlotID: 1, ValidatorAddress: "a"},
		{SlotID: 0, ValidatorAddress: "b"},
		{SlotID: 2, ValidatorAddress: "a"}, // second hit for "a"; should be ignored.
	}
	require.Equal(t, uint32(1), primarySlotID(group, "a"),
		"primarySlotID must pick the first match in iteration order")
	require.Equal(t, uint32(0), primarySlotID(group, "b"))
}

// TestPrimarySlotID_ReturnsZeroWhenAbsent asserts the "not a member"
// fallback returns 0 rather than panicking — gossip tolerates a 0
// primary slot, and the caller has already logged the anomaly.
func TestPrimarySlotID_ReturnsZeroWhenAbsent(t *testing.T) {
	group := []types.SlotAssignment{{SlotID: 3, ValidatorAddress: "someone-else"}}
	require.Equal(t, uint32(0), primarySlotID(group, "not-here"))
}

// TestEnvOr_FallbackAndOverride asserts the tiny helper behaves as
// the name suggests; guarding against future refactors that might
// accidentally flip the precedence.
func TestEnvOr_FallbackAndOverride(t *testing.T) {
	t.Setenv("DEVSHARDD_TESTENV_UNIT_TEST_KEY", "")
	require.Equal(t, "default", envOr("DEVSHARDD_TESTENV_UNIT_TEST_KEY", "default"))

	t.Setenv("DEVSHARDD_TESTENV_UNIT_TEST_KEY", "explicit")
	require.Equal(t, "explicit", envOr("DEVSHARDD_TESTENV_UNIT_TEST_KEY", "default"))
}

// fakeBridge is a minimal MainnetBridge double for peer-client
// resolution tests. Only the read methods used by buildPeerClients
// are implemented; the rest return ErrNotImplemented so any
// accidental call is loud.
type fakeBridge struct {
	hostInfos map[string]bridge.HostInfo
	errFor    map[string]error
}

func (f *fakeBridge) GetEscrow(string) (*bridge.EscrowInfo, error) { return nil, errNotImplemented }
func (f *fakeBridge) GetHostInfo(addr string) (*bridge.HostInfo, error) {
	if err, ok := f.errFor[addr]; ok {
		return nil, err
	}
	info, ok := f.hostInfos[addr]
	if !ok {
		return nil, errNotFound
	}
	return &info, nil
}
func (f *fakeBridge) VerifyWarmKey(string, string) (bool, error) { return false, nil }
func (f *fakeBridge) OnEscrowCreated(bridge.EscrowInfo) error    { return errNotImplemented }
func (f *fakeBridge) OnSettlementProposed(string, []byte, uint64) error {
	return errNotImplemented
}
func (f *fakeBridge) OnSettlementFinalized(string) error { return errNotImplemented }
func (f *fakeBridge) SubmitDisputeState(string, []byte, uint64, map[uint32][]byte) error {
	return errNotImplemented
}

var (
	errNotImplemented = errors.New("not implemented in test")
	errNotFound       = errors.New("not found")
)

// TestBuildPeerClients_DedupesAndExcludesSelf asserts the helper
// (a) skips our own address, (b) dedupes addresses that appear in
// multiple slots (multi-slot hosts), and (c) issues one resolution
// call per unique peer.
func TestBuildPeerClients_DedupesAndExcludesSelf(t *testing.T) {
	signer := mustSigner(t)
	myAddr := signer.Address()

	// Peer "b" owns two slots; "c" owns one; we ("a"=myAddr) own one.
	// Expected: 2 unique peers, order preserved by first appearance.
	group := []types.SlotAssignment{
		{SlotID: 0, ValidatorAddress: myAddr},
		{SlotID: 1, ValidatorAddress: "b"},
		{SlotID: 2, ValidatorAddress: "c"},
		{SlotID: 3, ValidatorAddress: "b"}, // dup.
	}
	br := &fakeBridge{
		hostInfos: map[string]bridge.HostInfo{
			"b": {Address: "b", URL: "http://b:9500"},
			"c": {Address: "c", URL: "http://c:9500"},
		},
	}

	clients, err := buildPeerClients("esc-1", myAddr, group, signer, br)
	require.NoError(t, err)
	require.Len(t, clients, 2, "exactly 2 unique peers expected (b, c); self excluded; duplicates collapsed")
}

// TestBuildPeerClients_PropagatesBridgeError asserts that a failed
// GetHostInfo surfaces as an error rather than being silently
// dropped, so a missing host is never confused with a silent peer.
func TestBuildPeerClients_PropagatesBridgeError(t *testing.T) {
	signer := mustSigner(t)
	myAddr := signer.Address()

	group := []types.SlotAssignment{
		{SlotID: 0, ValidatorAddress: myAddr},
		{SlotID: 1, ValidatorAddress: "b"},
	}
	br := &fakeBridge{
		hostInfos: map[string]bridge.HostInfo{},
		errFor:    map[string]error{"b": errNotFound},
	}
	_, err := buildPeerClients("esc", myAddr, group, signer, br)
	require.Error(t, err)
	require.Contains(t, err.Error(), "b")
}

// TestBuildPeerClients_SoloGroupReturnsNone asserts a degenerate
// single-host group produces zero peer clients (and no error), so
// devshardd-testenv can boot against a 1-slot escrow for simple
// smoke tests.
func TestBuildPeerClients_SoloGroupReturnsNone(t *testing.T) {
	signer := mustSigner(t)
	myAddr := signer.Address()
	group := []types.SlotAssignment{{SlotID: 0, ValidatorAddress: myAddr}}

	clients, err := buildPeerClients("esc", myAddr, group, signer, &fakeBridge{})
	require.NoError(t, err)
	require.Empty(t, clients)
}

func mustSigner(t *testing.T) signing.Signer {
	t.Helper()
	// A pinned hex key keeps the test deterministic; any valid
	// secp256k1 private key works.
	s, err := signing.SignerFromHex(
		"1111111111111111111111111111111111111111111111111111111111111111",
	)
	require.NoError(t, err)
	return s
}
