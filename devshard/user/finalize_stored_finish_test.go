package user

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

type countingEngine struct {
	inner *stub.InferenceEngine
	runs  atomic.Int32
}

func (e *countingEngine) Execute(ctx context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	e.runs.Add(1)
	return e.inner.Execute(ctx, req)
}

var errHostUnreachableForTest = errors.New("host unreachable")

type executorLink struct {
	mu      sync.Mutex
	current HostClient
	hide    atomic.Bool
	drop    atomic.Bool
}

func (c *executorLink) set(client HostClient) {
	c.mu.Lock()
	c.current = client
	c.mu.Unlock()
}

func (c *executorLink) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	if c.drop.Load() {
		return nil, errHostUnreachableForTest
	}
	c.mu.Lock()
	client := c.current
	c.mu.Unlock()
	resp, err := client.Send(ctx, req, stream, receiptHandler)
	if err == nil && resp != nil && c.hide.Load() {
		resp.Mempool = nil
	}
	return resp, err
}

type restartFixture struct {
	gatewayStore *storage.SQLite
	hostStore    storage.Storage
	executorHost *host.Host
	hosts        []*signing.Secp256k1Signer
	user         *signing.Secp256k1Signer
	group        []types.SlotAssignment
	config       types.SessionConfig
	clients      []HostClient
	executor     *executorLink
	session      *Session
}

const restartBalance = uint64(100000)

func newRestartFixture(t *testing.T) *restartFixture {
	t.Helper()
	numHosts := 4
	gatewayStore, err := storage.NewSQLite(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { gatewayStore.Close() })

	f := &restartFixture{gatewayStore: gatewayStore, user: testutil.MustGenerateKey(t), executor: &executorLink{}}
	f.hosts = make([]*signing.Secp256k1Signer, numHosts)
	for i := range f.hosts {
		f.hosts[i] = testutil.MustGenerateKey(t)
	}
	f.group = testutil.MakeGroup(f.hosts)
	f.config = testutil.DefaultConfig(numHosts)
	f.hostStore = storage.NewObsRepairGate(storage.NewManagedStorage(storage.NewHybridStorage(
		testutil.MustMemoryStore(t, "escrow-1", f.user.Address(), f.config, f.group, restartBalance)), 3, nil))
	require.NoError(t, gatewayStore.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    f.user.Address(),
		Config:         f.config,
		Group:          f.group,
		InitialBalance: restartBalance,
	}))

	verifier := signing.NewSecp256k1Verifier()
	f.clients = make([]HostClient, numHosts)
	for i := 1; i < numHosts; i++ {
		sm := newTestStateMachine(t, "escrow-1", f.config, f.group, restartBalance, f.user.Address(), verifier)
		h, err := host.NewHost(sm, f.hosts[i], stub.NewInferenceEngine(), "escrow-1", f.group, nil, host.WithGrace(100))
		require.NoError(t, err)
		f.clients[i] = &InProcessClient{Host: h}
	}
	f.executorHost, _ = f.openExecutor(t, stub.NewInferenceEngine())
	f.executor.set(&InProcessClient{Host: f.executorHost})
	f.executor.hide.Store(true)
	f.clients[0] = f.executor

	userSM := newTestStateMachine(t, "escrow-1", f.config, f.group, restartBalance, f.user.Address(), verifier)
	f.session, err = NewSession(userSM, f.user, "escrow-1", f.group, f.clients, verifier, WithStorage(gatewayStore))
	require.NoError(t, err)

	ctx := context.Background()
	for i := 0; i < numHosts; i++ {
		_, err := f.session.SendInference(ctx, finalizeTestParams())
		require.NoError(t, err)
	}
	require.False(t, HasMsgFinish(f.session.PendingTxs(), 4))
	return f
}

func (f *restartFixture) openExecutor(t *testing.T, engine devshard.InferenceEngine) (*host.Host, *state.StateMachine) {
	t.Helper()
	sm, err := state.NewStateMachine("escrow-1", f.config, f.group, restartBalance, f.user.Address(), signing.NewSecp256k1Verifier(), f.hostStore,
		state.WithStateRootAndProtocolVersion(testutil.RuntimeTestVersion))
	require.NoError(t, err)
	meta, err := f.hostStore.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	if meta.LatestNonce > 0 {
		require.NoError(t, storage.ReadDiffPages(f.hostStore, "escrow-1", 1, meta.LatestNonce, func(page []types.DiffRecord) error {
			for _, rec := range page {
				if _, err := sm.ApplyLocalPersisted(rec.Nonce, rec.Txs); err != nil {
					return err
				}
			}
			return nil
		}))
	}
	h, err := host.NewHost(sm, f.hosts[0], engine, "escrow-1", f.group, nil, host.WithGrace(100), host.WithStorage(f.hostStore))
	require.NoError(t, err)
	return h, sm
}

func (f *restartFixture) restartExecutor(t *testing.T) (*host.Host, *state.StateMachine, *countingEngine) {
	t.Helper()
	f.executorHost.Close()
	engine := &countingEngine{inner: stub.NewInferenceEngine()}
	h, sm := f.openExecutor(t, engine)
	f.executorHost = h
	f.executor.set(&InProcessClient{Host: h})
	f.executor.hide.Store(false)
	return h, sm, engine
}

func requireSameSettlement(t *testing.T, want, got types.EscrowState) {
	t.Helper()
	require.Equal(t, want.Balance, got.Balance)
	for slot, hs := range want.HostStats {
		require.Equal(t, hs.Cost, got.HostStats[slot].Cost, "slot %d cost", slot)
	}
}

func referenceSettlement(t *testing.T) types.EscrowState {
	t.Helper()
	f := newRestartFixture(t)
	f.executor.hide.Store(false)
	require.NoError(t, f.session.Finalize(context.Background()))
	require.Equal(t, types.PhaseSettlement, f.session.StateMachine().Phase())
	return f.session.StateMachine().SnapshotState()
}

func TestFinalize_RestartedExecutorReloadsOwnFinish(t *testing.T) {
	reference := referenceSettlement(t)

	f := newRestartFixture(t)
	restored, _, engine := f.restartExecutor(t)
	require.True(t, HasMsgFinish(restored.MempoolTxs(), 4))

	require.NoError(t, f.session.Finalize(context.Background()))
	require.Equal(t, types.PhaseSettlement, f.session.StateMachine().Phase())
	requireSameSettlement(t, reference, f.session.StateMachine().SnapshotState())
	require.Zero(t, engine.runs.Load())
}

func TestFinalize_ResumeWithExecutorPendingAtRestore(t *testing.T) {
	reference := referenceSettlement(t)
	ctx := context.Background()

	f := newRestartFixture(t)
	_, restoredSM, engine := f.restartExecutor(t)
	rec, ok := restoredSM.GetInference(4)
	require.True(t, ok)
	require.Equal(t, types.StatusPending, rec.Status)

	f.executor.drop.Store(true)
	runFinalizeRounds(t, f.session, len(f.hosts))
	require.Equal(t, types.PhaseFinalizing, f.session.StateMachine().Phase())
	require.NoError(t, f.session.FlushSnapshot())

	recovered, _, err := RecoverSession(f.gatewayStore, f.user, signing.NewSecp256k1Verifier(), "escrow-1", testutil.RuntimeTestVersion, f.group, f.clients)
	require.NoError(t, err)
	require.Equal(t, types.PhaseFinalizing, recovered.StateMachine().Phase())
	require.False(t, HasMsgFinish(recovered.PendingTxs(), 4))

	f.executor.drop.Store(false)
	require.NoError(t, recovered.Finalize(ctx))
	require.Equal(t, types.PhaseSettlement, recovered.StateMachine().Phase())
	requireSameSettlement(t, reference, recovered.StateMachine().SnapshotState())
	require.Zero(t, engine.runs.Load())
}
