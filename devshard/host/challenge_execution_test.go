package host

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

type blockedChallengeEngine struct {
	started chan context.Context
	release chan struct{}
	calls   atomic.Int32
}

func (e *blockedChallengeEngine) Execute(ctx context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	e.calls.Add(1)
	e.started <- ctx
	select {
	case <-e.release:
		return stub.NewInferenceEngine().Execute(ctx, req)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestChallengeReceiptExecutionLifetime(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "request cancellation and duplicate"
		if timeout {
			name = "execution deadline"
		}
		t.Run(name, func(t *testing.T) {
			signer, user := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
			group := testutil.MakeGroup([]*signing.Secp256k1Signer{signer})
			cfg := testutil.DefaultConfig(1)
			cfg.ExecutionTimeout = 30
			if timeout {
				cfg.ExecutionTimeout = 2
			}
			sm, err := state.NewStateMachine("escrow-1", cfg, group, 10000, user.Address(), signing.NewSecp256k1Verifier(), testutil.MustMemoryStore(t, "escrow-1", user.Address(), cfg, group, 10000))
			require.NoError(t, err)
			engine := &blockedChallengeEngine{started: make(chan context.Context, 2), release: make(chan struct{}, 1)}
			defer close(engine.release)
			h, err := NewHost(sm, signer, engine, "escrow-1", group, nil, WithGrace(10))
			require.NoError(t, err)
			diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
			requestCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				receipt []byte
				err     error
			}
			returned := make(chan result, 1)
			before := time.Now()
			go func() {
				receipt, _, err := h.ChallengeReceipt(requestCtx, 1, defaultPayload(), []types.Diff{diff})
				returned <- result{receipt, err}
			}()
			select {
			case got := <-returned:
				require.NoError(t, got.err)
				require.NotEmpty(t, got.receipt)
			case <-time.After(time.Second):
				t.Fatal("receipt waited for execution")
			}
			after := time.Now()
			var execCtx context.Context
			select {
			case execCtx = <-engine.started:
			case <-time.After(time.Second):
				t.Fatal("execution did not start")
			}
			cancel()
			require.NoError(t, execCtx.Err(), "request cancellation must not stop execution")
			deadline, ok := execCtx.Deadline()
			require.True(t, ok)
			duration := time.Duration(cfg.ExecutionTimeout) * time.Second
			require.False(t, deadline.Before(before.Add(duration)))
			require.False(t, deadline.After(after.Add(duration)))
			receipt, _, err := h.ChallengeReceipt(context.Background(), 1, defaultPayload(), nil)
			require.NoError(t, err)
			require.NotEmpty(t, receipt)
			require.EqualValues(t, 1, engine.calls.Load())
			if timeout {
				select {
				case <-execCtx.Done():
					require.ErrorIs(t, execCtx.Err(), context.DeadlineExceeded)
				case <-time.After(3 * time.Second):
					t.Fatal("execution exceeded its deadline")
				}
			} else {
				engine.release <- struct{}{}
				require.Eventually(t, func() bool {
					for _, tx := range h.MempoolTxs() {
						if tx.GetFinishInference() != nil {
							return true
						}
					}
					return false
				}, time.Second, time.Millisecond)
			}
			require.Eventually(t, func() bool {
				h.mu.Lock()
				defer h.mu.Unlock()
				_, running := h.executing[1]
				return !running
			}, time.Second, time.Millisecond)
			if timeout {
				require.Empty(t, h.MempoolTxs())
			}
		})
	}
}
