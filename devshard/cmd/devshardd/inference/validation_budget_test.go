package inference

import (
	"context"
	"errors"
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
	"devshard/observability"
	"devshard/storage"

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

// creditTestEngine returns an engine spending from the process-local budget
// or from a shared store, and a count of the credits free to reserve.
func creditTestEngine(t *testing.T, shared bool, ml *mlnodeclient.Client, mgr *mlnodeclient.Manager) (*Engine, *sharedCreditFake, func() int) {
	t.Helper()
	e := newTestEngine(ml, mgr, nil)
	e.validationBudget = newValidationBudget(time.Hour)
	if !shared {
		return e, nil, func() int { return len(e.validationBudget.credits["m"]) }
	}
	store := &sharedCreditFake{}
	e.UseSharedValidationCredits(store, "gonka1p")
	return e, store, func() int { return store.free(creditKey("gonka1p", "m")) }
}

func TestValidationDispatchSpendsOneCreditPerAnswer(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, fallback := range []bool{false, true} {
			t.Run(fmt.Sprintf("shared=%t/fallback=%t", shared, fallback), func(t *testing.T) {
				var hits, acquisitions atomic.Int32
				replies := make(chan int, 4)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					code := http.StatusServiceUnavailable
					select {
					case code = <-replies:
					default:
					}
					w.WriteHeader(code)
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
				})
				mgr := mlnodeclient.NewManager(time.Hour)
				mgr.Observe("m", "one", srv.URL)
				mgr.Observe("m", "two", srv.URL)
				e, store, free := creditTestEngine(t, shared, ml, mgr)
				v := &Validator{engine: e}
				earn := func() { e.earnValidationCredit(context.Background(), "m") }

				// A 5xx rotates to the next node under the same credit.
				earn()
				replies <- http.StatusServiceUnavailable
				replies <- http.StatusOK
				resp, err := v.executeMLRequest(context.Background(), "m", "escrow-1", []byte(`{}`))
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				require.Equal(t, int32(2), hits.Load())
				require.Zero(t, free(), "the answered validation spent its one credit")

				before := acquisitions.Load()
				_, err = v.executeMLRequest(context.Background(), "m", "escrow-2", []byte(`{}`))
				require.ErrorIs(t, err, devshard.ErrValidationDeferred)
				require.Equal(t, before, acquisitions.Load(), "a validation without a credit never acquires a node")

				// A 4xx is an answer and spends the credit.
				earn()
				replies <- http.StatusBadRequest
				resp, err = v.executeMLRequest(context.Background(), "m", "escrow-3", []byte(`{}`))
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				require.Zero(t, free())

				// No node answers: the credit returns.
				earn()
				_, err = v.executeMLRequest(context.Background(), "m", "escrow-4", []byte(`{}`))
				require.Error(t, err)
				require.NotErrorIs(t, err, devshard.ErrValidationDeferred)
				require.Equal(t, 1, free())
				if store != nil {
					require.Equal(t, 1, store.total(creditKey("gonka1p", "m")))
				}
			})
		}
	}
}

func TestValidationCanceledDispatchRefundsCredit(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared=%t", shared), func(t *testing.T) {
			dispatched := make(chan struct{}, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// The server sees the client go away only after the body is read.
				_, _ = io.Copy(io.Discard, r.Body)
				dispatched <- struct{}{}
				<-r.Context().Done()
			}))
			defer srv.Close()
			ml := startEngineMLClient(t, &engineMockNM{
				acquireFunc: func(context.Context, *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
					return &nmgen.AcquireMLNodeResponse{LockId: "lock", NodeId: "node", Endpoint: srv.URL}, nil
				},
			})
			e, _, free := creditTestEngine(t, shared, ml, nil)
			e.earnValidationCredit(context.Background(), "m")
			v := &Validator{engine: e}
			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				<-dispatched
				cancel()
			}()
			_, err := v.executeMLRequest(ctx, "m", "escrow", []byte(`{}`))
			var classified *observability.ClassifiedError
			require.ErrorAs(t, err, &classified)
			require.Equal(t, observability.ReasonTimeout, classified.Reason)
			require.NotErrorIs(t, err, devshard.ErrValidationDeferred, "a canceled validation is not a missing credit")
			require.Equal(t, 1, free(), "a dispatch canceled before the ML node answered returns its credit")
		})
	}
}

func TestSharedCreditHoldIsRenewedWhileDispatching(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	ml := startEngineMLClient(t, &engineMockNM{
		acquireFunc: func(context.Context, *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			return &nmgen.AcquireMLNodeResponse{LockId: "lock", NodeId: "node", Endpoint: srv.URL}, nil
		},
	})
	e, store, free := creditTestEngine(t, true, ml, nil)
	e.creditHold = 30 * time.Millisecond
	e.earnValidationCredit(context.Background(), "m")
	v := &Validator{engine: e}
	done := make(chan error, 1)
	go func() {
		resp, err := v.executeMLRequest(context.Background(), "m", "escrow", []byte(`{}`))
		if err == nil {
			err = resp.Body.Close()
		}
		done <- err
	}()
	require.Eventually(t, func() bool { return store.renewCount() >= 3 }, 5*time.Second, 5*time.Millisecond)
	require.Zero(t, free(), "a renewed hold keeps the credit away from siblings")
	close(release)
	require.NoError(t, <-done)
	require.Zero(t, store.total(creditKey("gonka1p", "m")), "the answered validation spent the credit")
	renews := store.renewCount()
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, renews, store.renewCount(), "settling the credit stops the renewal")
}

func TestSharedCreditStoreErrorIsNotDeferral(t *testing.T) {
	// No ML client: attempting acquisition would panic.
	e := newTestEngine(nil, nil, nil)
	e.UseSharedValidationCredits(&sharedCreditFake{reserveErr: errors.New("db down")}, "gonka1p")
	v := &Validator{engine: e}
	_, err := v.executeMLRequest(context.Background(), "m", "escrow", []byte(`{}`))
	var classified *observability.ClassifiedError
	require.ErrorAs(t, err, &classified)
	require.Equal(t, observability.ReasonStorageErr, classified.Reason)
	require.NotErrorIs(t, err, devshard.ErrValidationDeferred)
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
	v := &Validator{engine: e}
	_, err := v.executeMLRequest(context.Background(), "m", "escrow", []byte(`{}`))
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
	store := &memoryPayloads{}
	phase := new(chain.Phase)
	phase.SetEpoch(5)
	e := NewEngine(ml, nil, nil, store, fixedChainParams{}, phase, nil, true)
	req := recoveryRequest(t, devshard.RecoveryNone, 5)
	_, err := e.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, e.validationBudget.credits["m"], 1)
	req.Recovery = devshard.RecoveryStoredOnly
	_, err = e.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, int32(1), hits.Load(), "stored recovery must not run the model")
	require.Len(t, e.validationBudget.credits["m"], 1, "replay must not mint credits")
	req.Recovery = devshard.RecoveryNone
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

func TestValidatorValidateDeferred(t *testing.T) {
	for _, exhaustedDuringFetch := range []bool{false, true} {
		t.Run(fmt.Sprint(exhaustedDuringFetch), func(t *testing.T) {
			req := faultReq(10)
			budget := newValidationBudget(time.Hour)
			if exhaustedDuringFetch {
				budget.earn(req.Model)
			}
			fetches := 0
			v := newFaultTestValidator(10, false, func(context.Context, devshard.ValidateRequest, string, uint64) ([]byte, []byte, error) {
				fetches++
				require.True(t, spendCredit(budget, req.Model))
				return []byte(`{"messages":[]}`), []byte(`{"id":"test","object":"chat.completion","choices":[{"index":0,"logprobs":{"content":[{"token":"42","logprob":-0.5,"top_logprobs":[{"token":"42","logprob":-0.5},{"token":"99","logprob":-1.5}]}]}}]}`), nil
			}, nil, nil)
			// No ML client: attempting acquisition or dispatch would panic.
			v.engine = &Engine{validationBudget: budget}
			result, err := v.Validate(context.Background(), req)
			require.Nil(t, result)
			require.ErrorIs(t, err, devshard.ErrValidationDeferred)
			var classified *observability.ClassifiedError
			require.NotErrorAs(t, err, &classified)
			if exhaustedDuringFetch {
				require.Equal(t, 1, fetches)
			} else {
				require.Zero(t, fetches)
			}
		})
	}
	wrapped := fmt.Errorf("read: %w", devshard.ErrValidationDeferred)
	require.Same(t, wrapped, classifyExecuteValidationErr(wrapped))
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
			v := &Validator{engine: e}
			_, err := v.executeMLRequest(context.Background(), "m", "escrow", []byte(`{}`))
			require.Error(t, err)
			require.NotErrorIs(t, err, devshard.ErrValidationDeferred)
			require.Len(t, e.validationBudget.credits["m"], 1, "no HTTP dispatch means no credit spent")
		})
	}
}

// sharedCreditFake mirrors the Postgres credit table: a reserved credit stays
// as a row under a hold until it is spent, refunded, or the hold lapses.
type sharedCreditFake struct {
	mu         sync.Mutex
	nextID     int64
	rows       map[int64]*fakeCreditRow
	renews     int
	reserveErr error
}

type fakeCreditRow struct {
	key   string
	token string
	until time.Time
}

func creditKey(participant, model string) string { return participant + "\x00" + model }

// freeRow must be called with mu held.
func (s *sharedCreditFake) freeRow(key string) (int64, bool) {
	var found int64
	for id, row := range s.rows {
		if row.key == key && (row.token == "" || !row.until.After(time.Now())) && (found == 0 || id < found) {
			found = id
		}
	}
	return found, found != 0
}

func (s *sharedCreditFake) free(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, row := range s.rows {
		if row.key == key && (row.token == "" || !row.until.After(time.Now())) {
			n++
		}
	}
	return n
}

func (s *sharedCreditFake) total(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, row := range s.rows {
		if row.key == key {
			n++
		}
	}
	return n
}

func (s *sharedCreditFake) renewCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renews
}

func (s *sharedCreditFake) EarnValidationCredit(_ context.Context, participant, model string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = make(map[int64]*fakeCreditRow)
	}
	s.nextID++
	s.rows[s.nextID] = &fakeCreditRow{key: creditKey(participant, model)}
	return nil
}

func (s *sharedCreditFake) ValidationCreditAvailable(_ context.Context, participant, model string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.freeRow(creditKey(participant, model))
	return ok, nil
}

func (s *sharedCreditFake) ReserveValidationCredit(_ context.Context, participant, model string, hold time.Duration) (storage.ValidationCreditHold, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reserveErr != nil {
		return storage.ValidationCreditHold{}, false, s.reserveErr
	}
	id, ok := s.freeRow(creditKey(participant, model))
	if !ok {
		return storage.ValidationCreditHold{}, false, nil
	}
	s.nextID++
	row := s.rows[id]
	row.token = fmt.Sprint("hold-", s.nextID)
	row.until = time.Now().Add(hold)
	return storage.ValidationCreditHold{ID: id, Token: row.token}, true, nil
}

func (s *sharedCreditFake) held(h storage.ValidationCreditHold) (*fakeCreditRow, bool) {
	row, ok := s.rows[h.ID]
	return row, ok && row.token == h.Token
}

func (s *sharedCreditFake) RenewValidationCredit(_ context.Context, h storage.ValidationCreditHold, hold time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.held(h)
	if !ok {
		return false, nil
	}
	s.renews++
	row.until = time.Now().Add(hold)
	return true, nil
}

func (s *sharedCreditFake) SpendValidationCredit(_ context.Context, h storage.ValidationCreditHold) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.held(h); ok {
		delete(s.rows, h.ID)
	}
	return nil
}

func (s *sharedCreditFake) RefundValidationCredit(_ context.Context, h storage.ValidationCreditHold) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row, ok := s.held(h); ok {
		row.token = ""
		row.until = time.Time{}
	}
	return nil
}

func TestSharedValidationCreditIsSpendableByTheOtherReplica(t *testing.T) {
	store := &sharedCreditFake{}
	earner := NewEngine(nil, nil, nil, nil, nil, nil, false)
	survivor := NewEngine(nil, nil, nil, nil, nil, nil, false)
	earner.UseSharedValidationCredits(store, "gonka1pair")
	survivor.UseSharedValidationCredits(store, "gonka1pair")

	require.False(t, survivor.creditAvailable("m"))
	earner.earnValidationCredit(context.Background(), "m")
	require.Empty(t, earner.validationBudget.credits["m"], "the shared store replaces the process-local budget")
	// A replica that has not probed yet must observe the sibling's earn.
	// survivor's negative probe is cached for creditProbeFresh.
	late := NewEngine(nil, nil, nil, nil, nil, nil, false)
	late.UseSharedValidationCredits(store, "gonka1pair")
	require.True(t, late.creditAvailable("m"))

	credit, err := late.reserveValidationCredit(context.Background(), observability.PathValidate, "m")
	require.NoError(t, err)
	_, err = earner.reserveValidationCredit(context.Background(), observability.PathValidate, "m")
	require.ErrorIs(t, err, devshard.ErrValidationDeferred, "the sibling holds the only credit")
	credit.refund()
	credit, err = earner.reserveValidationCredit(context.Background(), observability.PathValidate, "m")
	require.NoError(t, err, "a refund returns the credit to the shared balance")
	credit.spend()
	require.Zero(t, store.total(creditKey("gonka1pair", "m")))
}

func TestSharedCreditOfADeadReplicaReturnsWhenTheHoldLapses(t *testing.T) {
	store := &sharedCreditFake{}
	store.EarnValidationCredit(context.Background(), "gonka1pair", "m", time.Hour)
	// The dead replica reserved and never renewed, spent or refunded.
	dead, ok, err := store.ReserveValidationCredit(context.Background(), "gonka1pair", "m", 20*time.Millisecond)
	require.NoError(t, err)
	require.True(t, ok)
	survivor := NewEngine(nil, nil, nil, nil, nil, nil, false)
	survivor.UseSharedValidationCredits(store, "gonka1pair")
	_, err = survivor.reserveValidationCredit(context.Background(), observability.PathValidate, "m")
	require.ErrorIs(t, err, devshard.ErrValidationDeferred)
	time.Sleep(30 * time.Millisecond)
	credit, err := survivor.reserveValidationCredit(context.Background(), observability.PathValidate, "m")
	require.NoError(t, err, "a lapsed hold makes the credit spendable again")
	require.NoError(t, store.SpendValidationCredit(context.Background(), dead))
	require.Equal(t, 1, store.total(creditKey("gonka1pair", "m")), "the stale holder cannot spend the survivor's credit")
	credit.spend()
	require.Zero(t, store.total(creditKey("gonka1pair", "m")))
}

func TestLeaseValidatorNoCreditsDoesNotAcquire(t *testing.T) {
	v := &Validator{engine: &Engine{validationBudget: newValidationBudget(defaultValidationCreditTTL)}}
	leases := &stubLeases{} // Acquire would panic: it must not be called.
	c := NewLeaseValidator(v, new(chain.Phase), leases, testLeaseOwner(), time.Hour)
	require.False(t, c.CanValidate("m"))
	_, err := c.Validate(context.Background(), devshard.ValidateRequest{Model: "m"})
	require.ErrorIs(t, err, devshard.ErrValidationDeferred)
	require.Empty(t, leases.acquireEpochs)
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
