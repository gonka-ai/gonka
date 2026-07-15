package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The root-cause layer: the gateway store must open in WAL with a busy timeout
// so concurrent writes wait instead of failing with "database is locked".
func TestGatewayStoreUsesWALAndBusyTimeout(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	var journalMode string
	require.NoError(t, store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode))
	assert.Equal(t, "wal", strings.ToLower(journalMode))

	var busyTimeout int
	require.NoError(t, store.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout))
	assert.Equal(t, 5000, busyTimeout)
}

func TestWithDBRetry_RespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var attempts atomic.Int32
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := withDBRetry(ctx, func() error {
		attempts.Add(1)
		return errors.New("database is locked")
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, int(attempts.Load()), escrowWriteRetries)
}

func recoveryTestSettings() GatewaySettings {
	return GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		EscrowRotation: EscrowRotationSettings{
			Enabled:           true,
			SettlementEnabled: true,
			Models: []EscrowRotationModelSettings{{
				ModelID:       "Qwen/Test",
				TempCount:     1,
				TargetCount:   1,
				Amount:        1000,
				PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY",
			}},
		},
	}.WithTuningDefaults()
}

func stubRuntimeBuilder(t *testing.T) {
	t.Helper()
	saved := gatewayRuntimeBuilder
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, _, _ string, _ *PerfTracker) (*devshardRuntime, error) {
		return &devshardRuntime{id: cfg.ID, model: cfg.Model}, nil
	}
	t.Cleanup(func() { gatewayRuntimeBuilder = saved })
}

// stubCreateOnChain replaces the on-chain create with a fake that mimics the real
// flow: it invokes onPrepared(txHash) (so the commitment is written) and aborts
// without a result if that fails — exactly as CreateDevshardEscrow does.
func stubCreateOnChain(t *testing.T, txHash string, escrowID uint64) {
	t.Helper()
	saved := gatewayCreateEscrowOnChain
	gatewayCreateEscrowOnChain = func(_ *Gateway, _ context.Context, _ GatewaySettings, _ EscrowRotationModelSettings, onPrepared func(string) error) (*CreateDevshardEscrowResult, error) {
		if onPrepared != nil {
			if err := onPrepared(txHash); err != nil {
				return nil, err
			}
		}
		return &CreateDevshardEscrowResult{EscrowID: escrowID, TxHash: txHash}, nil
	}
	t.Cleanup(func() { gatewayCreateEscrowOnChain = saved })
}

func stubQueryTxEscrowID(t *testing.T, fn func(string) (uint64, bool, error)) {
	t.Helper()
	saved := gatewayQueryTxEscrowID
	gatewayQueryTxEscrowID = func(_ context.Context, _ GatewaySettings, txHash string) (uint64, bool, error) {
		return fn(txHash)
	}
	t.Cleanup(func() { gatewayQueryTxEscrowID = saved })
}

func newRecoveryGateway(t *testing.T) (*Gateway, *GatewayStore, GatewaySettings) {
	t.Helper()
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	settings := recoveryTestSettings()
	require.NoError(t, store.Initialize(settings, nil))
	stubRuntimeBuilder(t)
	g := &Gateway{store: store, runtimes: map[string]*devshardRuntime{}}
	return g, store, settings
}

func devshardIDs(t *testing.T, store *GatewayStore) map[string]GatewayDevshardState {
	t.Helper()
	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	return gatewayDevshardsByID(state.Devshards)
}

// Happy path: intent commitment is written before the chain tx, the escrow is
// persisted, and the commitment is cleared.
func TestCreateRotationEscrowIntentFirstThenPersistAndClear(t *testing.T) {
	g, store, settings := newRecoveryGateway(t)
	stubCreateOnChain(t, "TXAAA", 777)
	model := normalizedEscrowRotationModels(settings)[0]

	_, err := g.createRotationEscrow(context.Background(), settings, model, rotationRoleTemp, 10)
	require.NoError(t, err)

	require.Contains(t, devshardIDs(t, store), "777", "escrow persisted")
	commitments, err := store.LoadCommitments()
	require.NoError(t, err)
	assert.Empty(t, commitments, "commitment cleared after persist")
}

// Rotation-created escrows inherit the protocol version implied by the
// gateway-wide route prefix (DEVSHARD_ROUTE_PREFIX), both on the direct
// persist path and through commitment recovery.
func TestCreateRotationEscrowCarriesProtocolVersionFromRoutePrefix(t *testing.T) {
	g, store, settings := newRecoveryGateway(t)
	stubCreateOnChain(t, "TXPV1", 555)
	t.Setenv("DEVSHARD_ROUTE_PREFIX", "/devshard/v3")
	model := normalizedEscrowRotationModels(settings)[0]

	_, err := g.createRotationEscrow(context.Background(), settings, model, rotationRoleTemp, 10)
	require.NoError(t, err)

	record := devshardIDs(t, store)["555"]
	assert.Equal(t, "3", record.ProtocolVersion, "persisted escrow inherits protocol from route prefix")
}

// A route prefix whose version segment is not a protocol version (e.g. a named
// versiond runtime) keeps the empty/v1-default behavior; semver-like versions
// map by their major component.
func TestRotationEscrowProtocolVersionRouteMapping(t *testing.T) {
	t.Setenv("DEVSHARD_ROUTE_PREFIX", "/devshard/mainnet-canary")
	assert.Empty(t, rotationEscrowProtocolVersion())
	t.Setenv("DEVSHARD_ROUTE_PREFIX", "/devshard/v3")
	assert.Equal(t, "3", rotationEscrowProtocolVersion())
	t.Setenv("DEVSHARD_ROUTE_PREFIX", "/devshard/v2.1.0")
	assert.Equal(t, "2", rotationEscrowProtocolVersion())
	t.Setenv("DEVSHARD_ROUTE_PREFIX", "/devshard/3")
	assert.Equal(t, "3", rotationEscrowProtocolVersion())
}

func TestReconcileCommitmentsCarriesProtocolVersion(t *testing.T) {
	g, store, settings := newRecoveryGateway(t)
	require.NoError(t, store.SaveCommitment(GatewayEscrowCommitment{
		TxHash: "TXPV2", Model: "Qwen/Test", Role: rotationRoleTemp, Epoch: 11,
		PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY", ProtocolVersion: "3",
	}))

	commitments, err := store.LoadCommitments()
	require.NoError(t, err)
	require.Len(t, commitments, 1)
	assert.Equal(t, "3", commitments[0].ProtocolVersion, "commitment round-trips protocol version")

	stubQueryTxEscrowID(t, func(string) (uint64, bool, error) { return 556, true, nil })
	g.reconcileCommitments(context.Background(), settings)

	record := devshardIDs(t, store)["556"]
	assert.Equal(t, "3", record.ProtocolVersion, "recovered escrow keeps protocol version")
}

// If the intent commitment cannot be written, no chain tx is broadcast and no
// escrow is created — the safe failure.
func TestCreateRotationEscrowAbortsWhenCommitmentWriteFails(t *testing.T) {
	g, store, settings := newRecoveryGateway(t)
	stubCreateOnChain(t, "TXBBB", 778)
	model := normalizedEscrowRotationModels(settings)[0]

	// Make every write fail, as a locked/read-only DB would.
	_, err := store.db.Exec("PRAGMA query_only=ON")
	require.NoError(t, err)

	_, err = g.createRotationEscrow(context.Background(), settings, model, rotationRoleTemp, 10)
	require.Error(t, err)

	_, _ = store.db.Exec("PRAGMA query_only=OFF")
	assert.NotContains(t, devshardIDs(t, store), "778", "no escrow created when intent write fails")
	commitments, _ := store.LoadCommitments()
	assert.Empty(t, commitments)
}

// If the post-create persist fails, the commitment survives and reconcile later
// recovers the escrow from chain by its tx hash — escrow_id is never lost.
func TestCreateRotationEscrowPersistFailureRecoversViaCommitment(t *testing.T) {
	g, store, settings := newRecoveryGateway(t)
	stubCreateOnChain(t, "TXCCC", 888)
	model := normalizedEscrowRotationModels(settings)[0]

	// Force the persist to fail (runtime build error) on the create path.
	savedBuilder := gatewayRuntimeBuilder
	gatewayRuntimeBuilder = func(RuntimeConfig, string, string, *PerfTracker) (*devshardRuntime, error) {
		return nil, errors.New("persist boom")
	}
	_, err := g.createRotationEscrow(context.Background(), settings, model, rotationRoleTemp, 10)
	require.Error(t, err)
	gatewayRuntimeBuilder = savedBuilder // restore (stubRuntimeBuilder's success)

	require.NotContains(t, devshardIDs(t, store), "888", "not persisted yet")
	commitments, err := store.LoadCommitments()
	require.NoError(t, err)
	require.Len(t, commitments, 1, "commitment kept for recovery")
	assert.Equal(t, "TXCCC", commitments[0].TxHash)

	// Reconcile: chain resolves the tx hash to the escrow_id.
	stubQueryTxEscrowID(t, func(txHash string) (uint64, bool, error) {
		assert.Equal(t, "TXCCC", txHash)
		return 888, true, nil
	})
	g.reconcileCommitments(context.Background(), settings)

	assert.Contains(t, devshardIDs(t, store), "888", "recovered via commitment")
	commitments, _ = store.LoadCommitments()
	assert.Empty(t, commitments, "commitment cleared after recovery")
}

// Reconcile persists an escrow for a pending commitment and clears it.
func TestReconcileCommitmentsRecoversFromChain(t *testing.T) {
	g, store, settings := newRecoveryGateway(t)
	require.NoError(t, store.SaveCommitment(GatewayEscrowCommitment{
		TxHash: "TXDDD", Model: "Qwen/Test", Role: rotationRoleTemp, Epoch: 11, PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY",
	}))
	stubQueryTxEscrowID(t, func(string) (uint64, bool, error) { return 999, true, nil })

	g.reconcileCommitments(context.Background(), settings)

	assert.Contains(t, devshardIDs(t, store), "999")
	commitments, _ := store.LoadCommitments()
	assert.Empty(t, commitments)
}

// A commitment whose tx committed but failed (no escrow created) is cleared
// immediately — the failure is final, the tx can never produce an escrow.
func TestReconcileCommitmentsClearsWhenTxCommittedFailed(t *testing.T) {
	g, store, settings := newRecoveryGateway(t)
	require.NoError(t, store.SaveCommitment(GatewayEscrowCommitment{TxHash: "TXEEE", Model: "Qwen/Test", Role: rotationRoleTemp}))
	stubQueryTxEscrowID(t, func(string) (uint64, bool, error) { return 0, false, nil })

	g.reconcileCommitments(context.Background(), settings)

	commitments, _ := store.LoadCommitments()
	assert.Empty(t, commitments, "commitment cleared when tx committed but created no escrow")
}

// A fresh commitment whose tx is not (yet) on chain must be KEPT: an unordered
// tx can still land until its TTL elapses, and a fresh 404 is usually mempool /
// index lag, not a failed broadcast. Clearing now would orphan a real escrow.
func TestReconcileCommitmentsKeepsFreshTxNotFound(t *testing.T) {
	g, store, settings := newRecoveryGateway(t)
	require.NoError(t, store.SaveCommitment(GatewayEscrowCommitment{TxHash: "TXFRESH", Model: "Qwen/Test", Role: rotationRoleTemp}))
	stubQueryTxEscrowID(t, func(string) (uint64, bool, error) { return 0, false, errTxNotFound })

	g.reconcileCommitments(context.Background(), settings)

	commitments, err := store.LoadCommitments()
	require.NoError(t, err)
	require.Len(t, commitments, 1, "fresh not-found commitment retained until its tx can no longer land")
	assert.Equal(t, "TXFRESH", commitments[0].TxHash)
}

// Once the commitment is older than the unordered-tx window, a not-found tx can
// never land — only then is it safe to clear.
func TestReconcileCommitmentsClearsExpiredTxNotFound(t *testing.T) {
	g, store, settings := newRecoveryGateway(t)
	require.NoError(t, store.SaveCommitment(GatewayEscrowCommitment{
		TxHash:    "TXOLD",
		Model:     "Qwen/Test",
		Role:      rotationRoleTemp,
		CreatedAt: time.Now().UTC().Add(-1 * time.Hour),
	}))
	stubQueryTxEscrowID(t, func(string) (uint64, bool, error) { return 0, false, errTxNotFound })

	g.reconcileCommitments(context.Background(), settings)

	commitments, _ := store.LoadCommitments()
	assert.Empty(t, commitments, "commitment cleared once the unordered tx can no longer land")
}

// A transient chain error leaves the commitment for the next pass.
func TestReconcileCommitmentsKeepsCommitmentOnChainError(t *testing.T) {
	g, store, settings := newRecoveryGateway(t)
	require.NoError(t, store.SaveCommitment(GatewayEscrowCommitment{TxHash: "TXFFF", Model: "Qwen/Test", Role: rotationRoleTemp}))
	stubQueryTxEscrowID(t, func(string) (uint64, bool, error) { return 0, false, errors.New("chain unreachable") })

	g.reconcileCommitments(context.Background(), settings)

	commitments, err := store.LoadCommitments()
	require.NoError(t, err)
	require.Len(t, commitments, 1, "commitment retained when chain cannot be queried")
	assert.Equal(t, "TXFFF", commitments[0].TxHash)
}
