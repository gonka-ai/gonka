package inference

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"common/chain"
	mlnodeclient "common/nodemanager"
	nmgen "common/nodemanager/gen"
	"devshard"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestValidationBudgetExpiryAndModels(t *testing.T) {
	now := time.Unix(1000, 0)
	b := newValidationBudget(defaultValidationCreditTTL)
	b.now = func() time.Time { return now }
	require.False(t, spendCredit(b, "a"), "start empty")
	b.earn("a")
	now = now.Add(30 * time.Minute)
	b.earn("a")
	require.False(t, spendCredit(b, "b"), "models cannot borrow credits")
	now = now.Add(30 * time.Minute)
	require.True(t, spendCredit(b, "a"), "the second credit is still valid")
	require.False(t, spendCredit(b, "a"), "the first credit expired at its own deadline")
	b.earn("a")
	require.True(t, spendCredit(b, "a"))
	require.False(t, spendCredit(b, "a"), "a spent credit cannot be reused")
}

func TestValidationBudgetHasNoBalanceOrConcurrencyCap(t *testing.T) {
	b := newValidationBudget(time.Hour)
	for i := 0; i < 1000; i++ {
		b.earn("m")
	}
	var wg sync.WaitGroup
	var spent atomic.Int32
	for i := 0; i < 1200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if spendCredit(b, "m") {
				spent.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1000), spent.Load())
	require.False(t, spendCredit(b, "m"))
}

func TestValidationDispatchChargesRetriesAcrossEscrows(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("fallback=%t", fallback), func(t *testing.T) {
			var hits, releases, acquisitions atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if hits.Add(1) == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				_, _ = io.WriteString(w, "ok")
			}))
			defer srv.Close()
			ml := startEngineMLClient(t, &engineMockNM{
				acquireFunc: func(_ context.Context, req *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
					acquisitions.Add(1)
					if fallback {
						return nil, status.Error(codes.Unavailable, "offline")
					}
					return &nmgen.AcquireMLNodeResponse{LockId: "lock", NodeId: fmt.Sprint(len(req.ExcludedNodes)), Endpoint: srv.URL}, nil
				},
				releaseFunc: func(_ context.Context, req *nmgen.ReleaseMLNodeRequest) (*nmgen.ReleaseMLNodeResponse, error) {
					releases.Add(1)
					return &nmgen.ReleaseMLNodeResponse{}, nil
				},
			})
			mgr := mlnodeclient.NewManager(time.Hour)
			mgr.Observe("m", "one", srv.URL)
			mgr.Observe("m", "two", srv.URL)
			e := newTestEngine(ml, mgr, nil)
			e.validationBudget = newValidationBudget(time.Hour)
			v := &Validator{phase: testPhase(1), engine: e}
			// Failed first HTTP attempt spends the only credit; the retry must defer.
			e.validationBudget.earn("m")
			_, err := v.executeMLRequest(context.Background(), "m", "escrow-1", []byte(`{}`), 1)
			require.ErrorIs(t, err, devshard.ErrValidationDeferred)
			require.Equal(t, int32(1), hits.Load())
			require.Empty(t, e.validationBudget.credits["m"])
			e.validationBudget.earn("m")
			e.validationBudget.earn("m")
			resp, err := v.executeMLRequest(context.Background(), "m", "escrow-2", []byte(`{}`), 1)
			require.NoError(t, err)
			// The remaining credit may be spent concurrently by a different escrow.
			other, err := v.executeMLRequest(context.Background(), "m", "escrow-3", []byte(`{}`), 1)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.NoError(t, other.Body.Close())
			_, err = v.executeMLRequest(context.Background(), "m", "escrow-4", []byte(`{}`), 1)
			require.ErrorIs(t, err, devshard.ErrValidationDeferred, "all escrows share the same credits")
			require.Equal(t, int32(3), hits.Load())
			require.Empty(t, e.validationBudget.credits["m"])
			require.Equal(t, int32(3), acquisitions.Load())
			if !fallback {
				require.Equal(t, int32(3), releases.Load(), "exhausted requests and retries must never acquire a node")
			}
		})
	}
}

func TestValidationFailedAcquisitionCostsNothing(t *testing.T) {
	ml := startEngineMLClient(t, &engineMockNM{
		acquireFunc: func(context.Context, *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			return nil, status.Error(codes.Unavailable, "offline")
		},
	})
	e := newTestEngine(ml, nil, nil)
	e.validationBudget = newValidationBudget(60 * time.Minute)
	e.validationBudget.earn("m")
	v := &Validator{phase: testPhase(1), engine: e}
	_, err := v.executeMLRequest(context.Background(), "m", "escrow", []byte(`{}`), 1)
	require.Error(t, err)
	require.Len(t, e.validationBudget.credits["m"], 1)
}

func TestOnlyFreshSuccessfulExecutionEarnsCredit(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1}}`)
	}))
	defer srv.Close()
	ml := startEngineMLClient(t, &engineMockNM{
		acquireFunc: func(context.Context, *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			return &nmgen.AcquireMLNodeResponse{LockId: "lock", NodeId: "node", Endpoint: srv.URL}, nil
		},
	})
	store := &recordingPayloadStore{}
	phase := new(chain.Phase)
	phase.SetEpoch(5)
	e := NewEngine(ml, nil, nil, store, fixedChainParams{}, phase)
	req := devshard.ExecuteRequest{InferenceID: 1, EscrowID: "e", Model: "m", Prompt: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)}
	_, err := e.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, e.validationBudget.credits["m"], 1)
	// A malformed prompt fails normal execution without earning a credit.
	req.Prompt = []byte(`{`)
	_, err = e.Execute(context.Background(), req)
	require.Error(t, err)
	require.Len(t, e.validationBudget.credits["m"], 1)
}

func spendCredit(b *validationBudget, model string) bool {
	_, ok := b.reserve(model)
	return ok
}

func TestValidationBudgetRefundPreservesExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	b := newValidationBudget(time.Hour)
	b.now = func() time.Time { return now }
	b.earn("m")
	first, ok := b.reserve("m")
	require.True(t, ok)
	now = now.Add(30 * time.Minute)
	b.earn("m")
	second, ok := b.reserve("m")
	require.True(t, ok)
	second()
	first()
	first() // A reservation can only be returned once.
	require.Len(t, b.credits["m"], 2)
	now = now.Add(30 * time.Minute)
	require.True(t, spendCredit(b, "m"))
	require.False(t, spendCredit(b, "m"), "refund must not extend the first credit")
	b.earn("m")
	expired, ok := b.reserve("m")
	require.True(t, ok)
	now = now.Add(time.Hour)
	expired()
	require.False(t, b.available("m"), "expired reservations cannot be revived")
}

func TestValidationRequestBuildFailureRefundsCredit(t *testing.T) {
	for _, endpoint := range []string{"://invalid", "/relative", "ftp://node", "http://"} {
		t.Run(endpoint, func(t *testing.T) {
			ml := startEngineMLClient(t, &engineMockNM{
				acquireFunc: func(context.Context, *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
					return &nmgen.AcquireMLNodeResponse{LockId: "lock", NodeId: "node", Endpoint: endpoint}, nil
				},
			})
			e := newTestEngine(ml, nil, nil)
			e.validationBudget = newValidationBudget(time.Hour)
			e.validationBudget.earn("m")
			v := &Validator{phase: testPhase(1), engine: e}
			_, err := v.executeMLRequest(context.Background(), "m", "escrow", []byte(`{}`), 1)
			require.Error(t, err)
			require.NotErrorIs(t, err, devshard.ErrValidationDeferred)
			require.Len(t, e.validationBudget.credits["m"], 1, "no HTTP dispatch means no credit spent")
		})
	}
}

func TestLeaseValidatorNoCreditsDoesNotAcquire(t *testing.T) {
	v := &Validator{phase: testPhase(1), engine: &Engine{validationBudget: newValidationBudget(defaultValidationCreditTTL)}}
	leases := &stubLeases{} // Acquire would panic: it must not be called.
	c := NewLeaseValidator(v, testPhase(1), leases, "host", time.Hour)
	require.False(t, c.CanValidate("m"))
	_, err := c.Validate(context.Background(), devshard.ValidateRequest{Model: "m"})
	require.ErrorIs(t, err, devshard.ErrValidationDeferred)
	require.Empty(t, leases.setResultCalls)
	v.engine.validationBudget.earn("m")
	require.True(t, c.CanValidate("m"))
}

func TestValidationBudgetSpendsOldestFirst(t *testing.T) {
	now := time.Unix(1000, 0)
	b := newValidationBudget(time.Hour)
	b.now = func() time.Time { return now }
	b.earn("m")
	now = now.Add(30 * time.Minute)
	b.earn("m")
	require.True(t, spendCredit(b, "m"))
	now = now.Add(30 * time.Minute)
	require.True(t, spendCredit(b, "m"), "the newer credit must survive the first credit's expiry")
	require.False(t, spendCredit(b, "m"))
}

func TestValidatorNoCreditsDefersBeforeFetching(t *testing.T) {
	v := &Validator{phase: testPhase(1), engine: &Engine{validationBudget: newValidationBudget(defaultValidationCreditTTL)}}
	// No payload client: attempting a fetch would panic.
	result, err := v.Validate(context.Background(), devshard.ValidateRequest{Model: "m"})
	require.Nil(t, result)
	require.ErrorIs(t, err, devshard.ErrValidationDeferred)
	wrapped := fmt.Errorf("read: %w", devshard.ErrValidationDeferred)
	require.Same(t, wrapped, classifyExecuteValidationErr(wrapped))
}
