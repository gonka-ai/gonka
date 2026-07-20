package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/cmd/devshardd/session"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
	"devshard/validationpool"
)

func TestRegisterValidationShutdown_LIFOOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(step string) {
		mu.Lock()
		order = append(order, step)
		mu.Unlock()
	}

	store := &recordingCloser{}
	sched := validationpool.New(validationpool.Config{Dispatchers: 1, DeferJitter: -1},
		func(context.Context, validationpool.Job) error { return nil }, nil)

	mgr := session.NewHostManager(
		storage.NewMemory(),
		testutil.MustGenerateKey(t),
		stub.NewInferenceEngine(),
		&shutdownValEngine{},
		nil,
		testutil.RuntimeTestVersion,
		&shutdownMockBridge{},
		nil,
		nil,
	)

	var closers closeStack
	// Instrument the three registerValidationShutdown steps + discovery.
	closers.Add(func() {
		record("store")
		_ = store.Close()
	})
	closers.Add(func() {
		record("scheduler")
		_ = sched.Shutdown(context.Background())
	})
	closers.Add(func() {
		record("hosts")
		mgr.CloseHosts()
	})
	closers.Add(func() { record("discovery") })
	closers.Close()

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"discovery", "hosts", "scheduler", "store"}, order)
}

func TestApp_ShutdownClosesHostsThenScheduler(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(step string) {
		mu.Lock()
		order = append(order, step)
		mu.Unlock()
	}

	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := types.SessionConfig{
		RefusalTimeout: 60, ExecutionTimeout: 1200, TokenPrice: 1, VoteThreshold: 1, ValidationRate: 10000,
	}
	mem := testutil.MustMemoryStore(t, "escrow-shutdown", user.Address(), config, group, 100000)
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-shutdown", config, group, 100000, user.Address(), verifier, mem)
	require.NoError(t, err)

	release := make(chan struct{})
	var validateStarted atomic.Bool
	var h *host.Host
	sched := validationpool.New(validationpool.Config{Dispatchers: 1, SoftMaxOut: 4, DeferJitter: -1},
		func(ctx context.Context, _ validationpool.Job) error {
			validateStarted.Store(true)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		func(_ string, id uint64) {
			if h != nil {
				h.ClearValidating(id)
			}
		},
	)

	h, err = host.NewHost(sm, hosts[0], stub.NewInferenceEngine(), "escrow-shutdown", group, nil,
		host.WithGrace(10),
		host.WithValidator(&shutdownValEngine{}),
		host.WithValidationScheduler(sched),
	)
	require.NoError(t, err)

	finishForShutdown(t, h, hosts, user, "escrow-shutdown")
	h.Start()
	h.RescanValidationOffers()
	require.Eventually(t, func() bool { return validateStarted.Load() }, 2*time.Second, 10*time.Millisecond)

	mgr := session.NewHostManager(mem, hosts[0], stub.NewInferenceEngine(), &shutdownValEngine{}, nil,
		testutil.RuntimeTestVersion, &shutdownMockBridge{}, nil, nil)
	mgr.SetValidationScheduler(sched)

	store := &recordingCloser{}
	var closers closeStack

	// Same order as registerValidationShutdown + discovery cancel in app.go.
	closers.Add(func() {
		record("store")
		_ = store.Close()
	})
	closers.Add(func() {
		record("scheduler")
		_ = sched.Shutdown(context.Background())
	})
	closers.Add(func() {
		record("hosts")
		h.Close()
		mgr.CloseHosts()
	})
	closers.Add(func() { record("discovery") })

	closers.Close()
	close(release)

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	require.Equal(t, []string{"discovery", "hosts", "scheduler", "store"}, got)
	assert.False(t, h.IsValidating(1), "host close must clear validating")
}

func TestRegisterValidationShutdown_HelperWiresStoreSchedulerHosts(t *testing.T) {
	store := &recordingCloser{}
	sched := validationpool.New(validationpool.Config{Dispatchers: 1, DeferJitter: -1},
		func(context.Context, validationpool.Job) error { return nil }, nil)
	mgr := session.NewHostManager(
		storage.NewMemory(),
		testutil.MustGenerateKey(t),
		stub.NewInferenceEngine(),
		&shutdownValEngine{},
		nil,
		testutil.RuntimeTestVersion,
		&shutdownMockBridge{},
		nil,
		nil,
	)

	var closers closeStack
	registerValidationShutdown(&closers, mgr, sched, store)
	require.Len(t, closers, 3)

	closers.Close()
	assert.True(t, store.closed.Load(), "store must close on stack drain")
}

type recordingCloser struct {
	onClose func()
	closed  atomic.Bool
}

func (c *recordingCloser) Close() error {
	if c.closed.CompareAndSwap(false, true) && c.onClose != nil {
		c.onClose()
	}
	return nil
}

type shutdownValEngine struct{}

func (shutdownValEngine) Validate(context.Context, devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	return &devshardpkg.ValidateResult{Valid: true}, nil
}

type shutdownMockBridge struct{}

func (shutdownMockBridge) OnEscrowCreated(bridge.EscrowInfo) error { return bridge.ErrNotImplemented }
func (shutdownMockBridge) OnSettlementProposed(string, []byte, uint64) error {
	return bridge.ErrNotImplemented
}
func (shutdownMockBridge) OnSettlementFinalized(string) error           { return nil }
func (shutdownMockBridge) GetEscrow(string) (*bridge.EscrowInfo, error) { return nil, nil }
func (shutdownMockBridge) GetHostInfo(string) (*bridge.HostInfo, error) { return nil, nil }
func (shutdownMockBridge) GetValidationThreshold(uint64, string) (*bridge.Decimal, error) {
	return nil, bridge.ErrNotImplemented
}
func (shutdownMockBridge) VerifyWarmKey(string, string) (bool, error) { return false, nil }
func (shutdownMockBridge) SubmitDisputeState(string, []byte, uint64, map[uint32][]byte) error {
	return bridge.ErrNotImplemented
}

func finishForShutdown(t *testing.T, h *host.Host, hosts []*signing.Secp256k1Signer, user *signing.Secp256k1Signer, escrow string) {
	t.Helper()
	engine := stub.NewInferenceEngine()
	diff1 := testutil.SignDiff(t, user, escrow, 1, []*types.DevshardTx{testutil.StartTx(1)})
	_, err := h.HandleRequest(context.Background(), host.HostRequest{Diffs: []types.Diff{diff1}})
	require.NoError(t, err)
	execSig := testutil.SignExecutorReceipt(t, hosts[1], escrow, 1, testutil.TestPromptHash[:], "llama", 100, 50, 1000, 2000)
	confirmTx := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 2000,
	}}}
	finishMsg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: engine.ResponseHash, InputTokens: 80, OutputTokens: 40,
		ExecutorSlot: 1, EscrowId: escrow,
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[1], finishMsg)
	diff2 := testutil.SignDiff(t, user, escrow, 2, []*types.DevshardTx{confirmTx})
	diff3 := testutil.SignDiff(t, user, escrow, 3, []*types.DevshardTx{
		{Tx: &types.DevshardTx_FinishInference{FinishInference: finishMsg}},
	})
	_, err = h.HandleRequest(context.Background(), host.HostRequest{Diffs: []types.Diff{diff2, diff3}})
	require.NoError(t, err)
}
