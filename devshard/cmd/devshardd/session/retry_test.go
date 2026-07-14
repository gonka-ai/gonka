package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"common/chain"
	mlnodeclient "common/nodemanager"

	devshardpkg "devshard"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
	"devshard/validationpool"
)

// --- stubs ---

type stubStaleLeaseStore struct {
	acquireFn      func(ctx context.Context, escrowId, instanceAddr string, ttl time.Duration) (uint64, uint64, error)
	setResultFn    func(ctx context.Context, escrowId string, inferenceId uint64, status storage.LeaseStatus, instanceAddr string) error
	setResultCalls []string
}

func (s *stubStaleLeaseStore) AcquireOneStale(ctx context.Context, escrowId, instanceAddr string, ttl time.Duration) (uint64, uint64, error) {
	return s.acquireFn(ctx, escrowId, instanceAddr, ttl)
}

func (s *stubStaleLeaseStore) SetResult(ctx context.Context, escrowId string, inferenceId uint64, status storage.LeaseStatus, instanceAddr string) error {
	s.setResultCalls = append(s.setResultCalls, fmt.Sprintf("%s/%d/%s", escrowId, inferenceId, status))
	if s.setResultFn != nil {
		return s.setResultFn(ctx, escrowId, inferenceId, status, instanceAddr)
	}
	return nil
}

type stubSessionManager struct {
	ids  []string
	host *host.Host
}

func (s *stubSessionManager) ActiveEscrowIDs() []string { return s.ids }
func (s *stubSessionManager) ExistingHost(_ string) (*host.Host, bool) {
	if s.host == nil {
		return nil, false
	}
	return s.host, true
}

type stubHostSnap struct {
	state types.EscrowState
	group []types.SlotAssignment
}

func (s *stubHostSnap) SnapshotState() types.EscrowState { return s.state }
func (s *stubHostSnap) Group() []types.SlotAssignment    { return s.group }

type captureOfferScheduler struct {
	mu   sync.Mutex
	jobs []validationpool.Job
}

func (c *captureOfferScheduler) Offer(job validationpool.Job) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.jobs = append(c.jobs, job)
	return true
}
func (c *captureOfferScheduler) ForgetHost(string) {}

func (c *captureOfferScheduler) snapshot() []validationpool.Job {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]validationpool.Job(nil), c.jobs...)
}

// --- buildValidateRequest tests ---

func TestBuildValidateRequest_InferenceNotFound(t *testing.T) {
	h := &stubHostSnap{
		state: types.EscrowState{Inferences: map[uint64]*types.InferenceRecord{}},
	}
	_, ok := buildValidateRequest(h, "escrow-1", 99, 0)
	assert.False(t, ok)
}

func TestBuildValidateRequest_WrongStatus(t *testing.T) {
	h := &stubHostSnap{
		state: types.EscrowState{
			Inferences: map[uint64]*types.InferenceRecord{
				10: {Status: types.StatusPending},
			},
		},
	}
	_, ok := buildValidateRequest(h, "escrow-1", 10, 5)
	assert.False(t, ok)
}

func TestBuildValidateRequest_HappyPath(t *testing.T) {
	h := &stubHostSnap{
		state: types.EscrowState{
			Inferences: map[uint64]*types.InferenceRecord{
				7: {
					Status:       types.StatusFinished,
					ExecutorSlot: 2,
					Model:        "llama-3",
					PromptHash:   []byte("ph"),
					ResponseHash: []byte("rh"),
					InputTokens:  100,
					OutputTokens: 50,
				},
			},
		},
		group: []types.SlotAssignment{
			{SlotID: 2, ValidatorAddress: "executor-addr"},
		},
	}
	req, ok := buildValidateRequest(h, "escrow-1", 7, 3)
	require.True(t, ok)
	assert.Equal(t, uint64(7), req.InferenceID)
	assert.Equal(t, "escrow-1", req.EscrowID)
	assert.Equal(t, "llama-3", req.Model)
	assert.Equal(t, []byte("ph"), req.PromptHash)
	assert.Equal(t, []byte("rh"), req.ResponseHash)
	assert.Equal(t, uint64(100), req.InputTokens)
	assert.Equal(t, uint64(50), req.OutputTokens)
	assert.Equal(t, "executor-addr", req.ExecutorAddress)
}

// --- retryForEscrow tests ---

// retryLoopWithCustomRetryOne creates a RetryLoop that overrides retryOne via a
// closure captured in the lease store, so we can test the loop logic without
// needing a real transport.Server.
//
// We test retryForEscrow by observing how many times AcquireOneStale is called
// and whether it terminates correctly.

func TestRetryForEscrow_NoStaleLeases(t *testing.T) {
	calls := 0
	leases := &stubStaleLeaseStore{
		acquireFn: func(_ context.Context, _, _ string, _ time.Duration) (uint64, uint64, error) {
			calls++
			return 0, 0, nil // no stale leases
		},
	}
	rl := &RetryLoop{
		leases:       leases,
		manager:      &stubSessionManager{},
		instanceAddr: "addr",
		leaseTTL:     DefaultLeaseTTL,
		interval:     DefaultRetryInterval,
	}
	rl.retryForEscrow(context.Background(), "escrow-1")
	assert.Equal(t, 1, calls, "should call AcquireOneStale once and stop")
}

func TestRetryForEscrow_AcquireError_Stops(t *testing.T) {
	calls := 0
	leases := &stubStaleLeaseStore{
		acquireFn: func(_ context.Context, _, _ string, _ time.Duration) (uint64, uint64, error) {
			calls++
			return 0, 0, errors.New("db error")
		},
	}
	rl := &RetryLoop{
		leases:       leases,
		manager:      &stubSessionManager{},
		instanceAddr: "addr",
		leaseTTL:     DefaultLeaseTTL,
	}
	rl.retryForEscrow(context.Background(), "escrow-1")
	assert.Equal(t, 1, calls, "should stop after first error")
}

func TestRetryForEscrow_LeaseFromPreviousEpochIsSkipped(t *testing.T) {
	callCount := 0
	leases := &stubStaleLeaseStore{
		acquireFn: func(_ context.Context, _, _ string, _ time.Duration) (uint64, uint64, error) {
			callCount++
			if callCount == 1 {
				return 1, 10, nil
			}
			return 0, 0, nil
		},
	}
	phase := new(chain.Phase)
	phase.Update(11, 0)
	rl := &RetryLoop{
		leases:       leases,
		manager:      &stubSessionManager{},
		phase:        phase,
		instanceAddr: "addr",
		leaseTTL:     DefaultLeaseTTL,
	}

	rl.retryForEscrow(context.Background(), "escrow-1")

	assert.Equal(t, 2, callCount)
	require.Len(t, leases.setResultCalls, 1)
	assert.Equal(t, "escrow-1/1/skipped", leases.setResultCalls[0])
}

func TestRetryForEscrow_SessionNotLoaded_LogsAndContinues(t *testing.T) {
	// First call returns a stale inference; second returns empty (done).
	callCount := 0
	leases := &stubStaleLeaseStore{
		acquireFn: func(_ context.Context, _, _ string, _ time.Duration) (uint64, uint64, error) {
			callCount++
			if callCount == 1 {
				return 1, 0, nil
			}
			return 0, 0, nil
		},
	}
	// ExistingHost returns false — session not loaded.
	mgr := &stubSessionManager{}
	rl := &RetryLoop{
		leases:       leases,
		manager:      mgr,
		instanceAddr: "addr",
		leaseTTL:     DefaultLeaseTTL,
	}
	// Should not panic; logs the error and tries next iteration.
	rl.retryForEscrow(context.Background(), "escrow-1")
	// callCount should be 2: first acquire returned 1 (retryOne failed), second returned 0.
	assert.Equal(t, 2, callCount)
}

func TestRetryLoop_OffersIntoScheduler(t *testing.T) {
	escrow := "escrow-retry-offer"
	capSched := &captureOfferScheduler{}
	h, hosts, user := newRetryTestHost(t, escrow)
	h, err := rebuildHostWithScheduler(t, hosts, user, escrow, capSched, &trackingEngine{valid: true}, nil, nil)
	require.NoError(t, err)
	finishInference(t, h, hosts, user, escrow) // before Start: no traffic Offer
	h.Start()
	t.Cleanup(h.Close)

	n := 0
	leases := &stubStaleLeaseStore{
		acquireFn: func(_ context.Context, _, _ string, _ time.Duration) (uint64, uint64, error) {
			n++
			if n == 1 {
				return 1, 1, nil
			}
			return 0, 0, nil
		},
	}
	rl := &RetryLoop{
		leases:       leases,
		manager:      &stubSessionManager{ids: []string{escrow}, host: h},
		instanceAddr: "addr",
		leaseTTL:     DefaultLeaseTTL,
	}
	rl.retryForEscrow(context.Background(), escrow)

	jobs := capSched.snapshot()
	require.Len(t, jobs, 1)
	assert.True(t, jobs[0].LeaseHeld)
	assert.False(t, jobs[0].LeaseClaimedAt.IsZero())
	assert.Equal(t, uint64(1), jobs[0].InferenceID)
	assert.Empty(t, leases.setResultCalls, "must not SetResult(submitted) in RetryLoop")
}

func TestRetryLoop_CountsTowardSoftMaxOut(t *testing.T) {
	escrow := "escrow-retry-cap"
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	var maxOut atomic.Int64

	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	var h *host.Host
	var sched *validationpool.Scheduler
	sched = validationpool.New(validationpool.Config{Dispatchers: 1, SoftMaxOut: 1, DeferJitter: -1},
		func(ctx context.Context, _ validationpool.Job) error {
			cur := sched.Outstanding()
			for {
				old := maxOut.Load()
				if cur <= old || maxOut.CompareAndSwap(old, cur) {
					break
				}
			}
			started <- struct{}{}
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
	t.Cleanup(func() {
		close(release)
		_ = sched.Shutdown(context.Background())
	})

	var err error
	h, err = rebuildHostWithScheduler(t, hosts, user, escrow, sched, &trackingEngine{valid: true}, nil, nil)
	require.NoError(t, err)
	// id%3==1 → executor slot 1 (peer). Nonce must equal inference id on Start.
	finishInference(t, h, hosts, user, escrow)         // ids/nonces 1..3
	finishInferenceAt(t, h, hosts, user, escrow, 4, 1) // start@4 confirm@5 finish@6
	h.Start()
	t.Cleanup(h.Close)

	acq := 0
	leases := &stubStaleLeaseStore{
		acquireFn: func(_ context.Context, _, _ string, _ time.Duration) (uint64, uint64, error) {
			acq++
			switch acq {
			case 1:
				return 1, 1, nil
			case 2:
				return 4, 1, nil
			default:
				return 0, 0, nil
			}
		},
	}
	rl := &RetryLoop{
		leases:       leases,
		manager:      &stubSessionManager{host: h},
		instanceAddr: "addr",
		leaseTTL:     DefaultLeaseTTL,
	}
	rl.retryForEscrow(context.Background(), escrow)

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("expected one in-flight validation")
	}
	time.Sleep(30 * time.Millisecond)
	assert.LessOrEqual(t, maxOut.Load(), int64(1))
	assert.Equal(t, int64(1), sched.Outstanding())
}

func TestRetryLoop_BusyDefersLikeNormal(t *testing.T) {
	escrow := "escrow-retry-busy"
	var attempts atomic.Int32
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	var h *host.Host
	eng := &sequenceEngine{fns: []func() (*devshardpkg.ValidateResult, error){
		func() (*devshardpkg.ValidateResult, error) {
			attempts.Add(1)
			return nil, mlnodeclient.ErrNoNodesAvailable
		},
		func() (*devshardpkg.ValidateResult, error) {
			attempts.Add(1)
			return &devshardpkg.ValidateResult{Valid: true}, nil
		},
	}}
	sched := validationpool.New(validationpool.Config{
		Dispatchers: 1, SoftMaxOut: 4, DeferDelay: 15 * time.Millisecond, DeferJitter: -1,
	}, func(ctx context.Context, job validationpool.Job) error {
		return h.RunScheduledValidation(ctx, job)
	}, func(_ string, id uint64) { h.ClearValidating(id) })
	t.Cleanup(func() { _ = sched.Shutdown(context.Background()) })

	var err error
	h, err = rebuildHostWithScheduler(t, hosts, user, escrow, sched, eng, eng, &stubSessionLeaseFinisher{owned: true})
	require.NoError(t, err)
	finishInference(t, h, hosts, user, escrow)
	h.Start()
	t.Cleanup(h.Close)

	n := 0
	leases := &stubStaleLeaseStore{
		acquireFn: func(_ context.Context, _, _ string, _ time.Duration) (uint64, uint64, error) {
			n++
			if n == 1 {
				return 1, 1, nil
			}
			return 0, 0, nil
		},
	}
	rl := &RetryLoop{leases: leases, manager: &stubSessionManager{host: h}, instanceAddr: "addr", leaseTTL: DefaultLeaseTTL}
	rl.retryForEscrow(context.Background(), escrow)

	require.Eventually(t, func() bool { return attempts.Load() >= 2 }, 2*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return !h.IsValidating(1) }, 2*time.Second, 10*time.Millisecond)
}

type stubSessionLeaseFinisher struct {
	owned bool
}

func (f *stubSessionLeaseFinisher) OwnsPendingLease(context.Context, string, uint64) (bool, error) {
	return f.owned, nil
}
func (f *stubSessionLeaseFinisher) MarkSubmitted(context.Context, string, uint64) error { return nil }
func (f *stubSessionLeaseFinisher) LeaseTTL() time.Duration                             { return 30 * time.Minute }

type trackingEngine struct{ valid bool }

func (e *trackingEngine) Validate(context.Context, devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	return &devshardpkg.ValidateResult{Valid: e.valid}, nil
}

type sequenceEngine struct {
	mu  sync.Mutex
	fns []func() (*devshardpkg.ValidateResult, error)
	i   int
}

func (e *sequenceEngine) Validate(context.Context, devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.i >= len(e.fns) {
		return &devshardpkg.ValidateResult{Valid: true}, nil
	}
	fn := e.fns[e.i]
	e.i++
	return fn()
}

func newRetryTestHost(t *testing.T, escrow string) (*host.Host, []*signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := types.SessionConfig{
		RefusalTimeout: 60, ExecutionTimeout: 1200, TokenPrice: 1, VoteThreshold: 1, ValidationRate: 10000,
	}
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine(escrow, config, group, 100000, user.Address(), verifier,
		testutil.MustMemoryStore(t, escrow, user.Address(), config, group, 100000))
	require.NoError(t, err)
	h, err := host.NewHost(sm, hosts[0], stub.NewInferenceEngine(), escrow, group, nil,
		host.WithGrace(10), host.WithValidator(&trackingEngine{valid: true}))
	require.NoError(t, err)
	return h, hosts, user
}

func rebuildHostWithScheduler(
	t *testing.T,
	hosts []*signing.Secp256k1Signer,
	user *signing.Secp256k1Signer,
	escrow string,
	sched host.ValidationScheduler,
	eng devshardpkg.ValidationEngine,
	direct devshardpkg.ValidationEngine,
	finisher devshardpkg.LeaseFinisher,
) (*host.Host, error) {
	t.Helper()
	group := testutil.MakeGroup(hosts)
	config := types.SessionConfig{
		RefusalTimeout: 60, ExecutionTimeout: 1200, TokenPrice: 1, VoteThreshold: 1, ValidationRate: 10000,
	}
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine(escrow, config, group, 100000, user.Address(), verifier,
		testutil.MustMemoryStore(t, escrow+"-b", user.Address(), config, group, 100000))
	if err != nil {
		return nil, err
	}
	opts := []host.HostOption{
		host.WithGrace(10),
		host.WithValidator(eng),
		host.WithValidationScheduler(sched),
	}
	if direct != nil {
		opts = append(opts, host.WithDirectValidator(direct))
	}
	if finisher != nil {
		opts = append(opts, host.WithLeaseFinisher(finisher))
	}
	return host.NewHost(sm, hosts[0], stub.NewInferenceEngine(), escrow, group, nil, opts...)
}

func finishInference(t *testing.T, h *host.Host, hosts []*signing.Secp256k1Signer, user *signing.Secp256k1Signer, escrow string) {
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

// finishInferenceAt applies Start/Confirm/Finish for inferenceID where
// Start nonce == inferenceID (chain rule). executorSlot must be a peer slot.
func finishInferenceAt(t *testing.T, h *host.Host, hosts []*signing.Secp256k1Signer, user *signing.Secp256k1Signer, escrow string, inferenceID uint64, executorSlot uint32) {
	t.Helper()
	engine := stub.NewInferenceEngine()
	require.Equal(t, inferenceID, h.LatestNonce()+1, "Start nonce must equal inference id")
	diff1 := testutil.SignDiff(t, user, escrow, inferenceID, []*types.DevshardTx{testutil.StartTx(inferenceID)})
	_, err := h.HandleRequest(context.Background(), host.HostRequest{Diffs: []types.Diff{diff1}})
	require.NoError(t, err)
	execSig := testutil.SignExecutorReceipt(t, hosts[executorSlot], escrow, inferenceID, testutil.TestPromptHash[:], "llama", 100, 50, 1000, 3000)
	confirmTx := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: inferenceID, ExecutorSig: execSig, ConfirmedAt: 3000,
	}}}
	diff2 := testutil.SignDiff(t, user, escrow, inferenceID+1, []*types.DevshardTx{confirmTx})
	finishMsg := &types.MsgFinishInference{
		InferenceId: inferenceID, ResponseHash: engine.ResponseHash, InputTokens: 80, OutputTokens: 40,
		ExecutorSlot: executorSlot, EscrowId: escrow,
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[executorSlot], finishMsg)
	diff3 := testutil.SignDiff(t, user, escrow, inferenceID+2, []*types.DevshardTx{
		{Tx: &types.DevshardTx_FinishInference{FinishInference: finishMsg}},
	})
	_, err = h.HandleRequest(context.Background(), host.HostRequest{Diffs: []types.Diff{diff2, diff3}})
	require.NoError(t, err)
}
