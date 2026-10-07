package user

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/storage"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

type memoryCompletionStore struct {
	mu                 sync.Mutex
	entries            map[uint64]InferenceCompletion
	saveErr, deleteErr error
}

func newMemoryCompletionStore() *memoryCompletionStore {
	return &memoryCompletionStore{entries: make(map[uint64]InferenceCompletion)}
}
func (m *memoryCompletionStore) SaveInferenceCompletion(_ string, e InferenceCompletion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	if prior, ok := m.entries[e.Nonce]; ok {
		if string(prior.Payload.Prompt) != string(e.Payload.Prompt) || prior.Payload.Model != e.Payload.Model {
			return ErrInferenceCompletionConflict
		}
		return nil
	}
	e.PreparedAt = time.Now().Add(-time.Hour).UnixNano()
	m.entries[e.Nonce] = e
	return nil
}
func (m *memoryCompletionStore) ListInferenceCompletions(_ string) ([]InferenceCompletion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var entries []InferenceCompletion
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	return entries, nil
}
func (m *memoryCompletionStore) DeleteInferenceCompletion(_ string, n uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.entries, n)
	return nil
}

type completionVerifierClient struct {
	HostClient
	*mockTimeoutVerifier
}

type completionFailedSendClient struct {
	HostClient
	*mockTimeoutVerifier
	failed atomic.Bool
}

func (c *completionFailedSendClient) Send(ctx context.Context, req host.HostRequest, w io.Writer, receipt func(*host.HostResponse)) (*host.HostResponse, error) {
	if c.failed.CompareAndSwap(false, true) {
		return nil, errors.New("temporary delivery failure")
	}
	return c.HostClient.Send(ctx, req, w, receipt)
}

func completionParams() InferenceParams {
	return InferenceParams{Model: "llama", Prompt: testutil.TestPrompt, InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}
}

func TestCompletionJournalFailurePreventsSignedStart(t *testing.T) {
	s, _, _ := setupSession(t, 3, 100000, 10)
	journal := newMemoryCompletionStore()
	journal.saveErr = errors.New("disk full")
	s.SetInferenceCompletionStore(journal)
	_, err := s.PrepareInference(completionParams())
	require.ErrorContains(t, err, "disk full")
	require.Zero(t, s.Nonce())
	require.Empty(t, s.Diffs())
	_, exists := s.sm.Inference(1)
	require.False(t, exists)
}

func TestCompletionFinalizeBlocksFailedVotesAndRetries(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_quorum", true: "cancellation"}[canceled], func(t *testing.T) {
			s, signers, _ := setupSession(t, 3, 100000, 10)
			journal := newMemoryCompletionStore()
			s.SetInferenceCompletionStore(journal)
			p, err := s.PrepareInference(completionParams())
			require.NoError(t, err)
			var voters []*mockTimeoutVerifier
			for i, client := range s.clients {
				voter := &mockTimeoutVerifier{signer: signers[i], group: s.group, slotIdx: i}
				voters = append(voters, voter)
				s.clients[i] = &completionVerifierClient{client, voter}
			}
			ctx := context.Background()
			if canceled {
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelCtx
			}
			require.ErrorIs(t, s.Finalize(ctx), ErrInferenceCompletionPending)
			require.Equal(t, types.PhaseActive, s.sm.Phase())
			rec, ok := s.sm.Inference(p.Nonce())
			require.True(t, ok)
			require.Equal(t, types.StatusPending, rec.Status)
			entries, err := journal.ListInferenceCompletions("escrow-1")
			require.NoError(t, err)
			require.Len(t, entries, 1)
			for _, v := range voters {
				v.accept = true
			}
			require.NoError(t, s.Finalize(context.Background()))
			require.Equal(t, types.PhaseSettlement, s.sm.Phase())
			after := s.sm.SnapshotState()
			require.Zero(t, after.HostStats[rec.ExecutorSlot].Cost)
			require.Equal(t, uint32(1), after.HostStats[rec.ExecutorSlot].Missed)
			entries, err = journal.ListInferenceCompletions("escrow-1")
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func TestCompletionRetryAfterLocalApplyAndFailedDelivery(t *testing.T) {
	s, signers, _ := setupSession(t, 3, 100000, 10)
	journal := newMemoryCompletionStore()
	s.SetInferenceCompletionStore(journal)
	p, err := s.PrepareInference(completionParams())
	require.NoError(t, err)
	for i, client := range s.clients {
		s.clients[i] = &completionFailedSendClient{HostClient: client, mockTimeoutVerifier: &mockTimeoutVerifier{accept: true, signer: signers[i], group: s.group, slotIdx: i}}
	}
	journal.deleteErr = errors.New("temporary journal write failure")
	require.ErrorIs(t, s.Finalize(context.Background()), ErrInferenceCompletionPending)
	rec, ok := s.sm.Inference(p.Nonce())
	require.True(t, ok)
	require.Equal(t, types.StatusTimedOut, rec.Status)
	require.Equal(t, types.PhaseActive, s.sm.Phase())
	journal.deleteErr = nil
	require.NoError(t, s.Finalize(context.Background()))
	require.Equal(t, uint32(1), s.sm.SnapshotState().HostStats[rec.ExecutorSlot].Missed)
}

func TestCompletionCrashBeforeStartAllowsDifferentNextRequest(t *testing.T) {
	s, _, _ := setupSession(t, 3, 100000, 10)
	journal := newMemoryCompletionStore()
	s.SetInferenceCompletionStore(journal)
	require.NoError(t, journal.SaveInferenceCompletion("escrow-1", InferenceCompletion{Nonce: 1, Payload: host.InferencePayload{Model: "orphan"}}))
	_, err := s.PrepareInference(completionParams())
	require.NoError(t, err)
	entries, err := journal.ListInferenceCompletions("escrow-1")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "llama", entries[0].Payload.Model)
}

func TestCompletionAmbiguousDurableStartCannotBeDiscarded(t *testing.T) {
	s, _, creator := setupSession(t, 3, 100000, 10)
	db := newTestStore(t)
	require.NoError(t, db.CreateSession(storage.CreateSessionParams{EscrowID: "escrow-1", Version: testutil.RuntimeTestVersion, CreatorAddr: creator.Address(), Config: s.sm.SnapshotState().Config, Group: s.group, InitialBalance: 100000}))
	s.store = db
	journal := newMemoryCompletionStore()
	s.SetInferenceCompletionStore(journal)
	_, err := s.PrepareInference(completionParams())
	require.NoError(t, err)
	s.nonce = 0
	fresh, _, _ := setupSession(t, 3, 100000, 10)
	fresh.store = db
	fresh.SetInferenceCompletionStore(journal)
	require.ErrorIs(t, fresh.Finalize(context.Background()), ErrInferenceCompletionPending)
	entries, err := journal.ListInferenceCompletions("escrow-1")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	params := completionParams()
	params.Model = "replacement"
	_, err = fresh.PrepareInference(params)
	require.ErrorIs(t, err, ErrInferenceCompletionConflict)
}

func TestCompletionFinalizeExcludesActiveSendAndNewAdmission(t *testing.T) {
	s, signers, _ := setupSession(t, 3, 100000, 10)
	s.SetInferenceCompletionStore(newMemoryCompletionStore())
	p, err := s.PrepareInference(completionParams())
	require.NoError(t, err)
	holding := &holdingClient{receiptSent: make(chan struct{}), release: make(chan struct{})}
	s.clients[p.HostIdx()] = holding
	sent := make(chan struct{})
	go func() { defer close(sent); _, _ = s.SendOnly(context.Background(), p, nil, nil) }()
	<-holding.receiptSent
	require.ErrorIs(t, s.Finalize(context.Background()), ErrInferenceCompletionPending)
	close(holding.release)
	<-sent
	entered, release := make(chan struct{}, 3), make(chan struct{})
	for i, client := range s.clients {
		s.clients[i] = &completionVerifierClient{client, &mockTimeoutVerifier{signer: signers[i], group: s.group, slotIdx: i, onVerify: func() { entered <- struct{}{}; <-release }}}
	}
	finalized := make(chan error, 1)
	go func() { finalized <- s.Finalize(context.Background()) }()
	<-entered
	_, err = s.PrepareInference(completionParams())
	require.ErrorIs(t, err, types.ErrSessionFinalizing)
	_, err = s.SendOnly(context.Background(), p, nil, nil)
	require.ErrorIs(t, err, types.ErrSessionFinalizing)
	close(release)
	require.ErrorIs(t, <-finalized, ErrInferenceCompletionPending)
}

type receiptEventClient struct {
	HostClient
	response *host.HostResponse
}

func (c *receiptEventClient) Send(_ context.Context, _ host.HostRequest, _ io.Writer, onReceipt func(*host.HostResponse)) (*host.HostResponse, error) {
	onReceipt(c.response)
	return c.response, nil
}
func TestReceiptNotificationsRequireExecutorAuthentication(t *testing.T) {
	for _, mode := range []string{"empty_envelope", "garbage", "zero_time", "valid", "canonical_duplicate"} {
		t.Run(mode, func(t *testing.T) {
			s, signers, _ := setupSession(t, 3, 100000, 10)
			p, err := s.PrepareInference(completionParams())
			require.NoError(t, err)
			inner := s.clients[p.HostIdx()].(*InProcessClient)
			response, err := inner.Host.HandleRequest(context.Background(), host.HostRequest{Diffs: p.catchUp, Nonce: p.Nonce(), Payload: p.Payload()})
			require.NoError(t, err)
			switch mode {
			case "empty_envelope":
				response.Receipt = nil
			case "garbage":
				response.Receipt = []byte("garbage")
			case "zero_time":
				response.ConfirmedAt = 0
			case "canonical_duplicate":
				receipt := testutil.SignExecutorReceipt(t, signers[p.HostIdx()], "escrow-1", p.Nonce(), testutil.TestPromptHash[:], "llama", 100, testutil.TestMaxTokens, 1000, response.ConfirmedAt)
				response.Receipt = receipt
				require.True(t, s.confirmStartOnReceipt(p.Nonce(), response))
				require.NoError(t, s.SendPendingDiff(context.Background()))
			}
			s.clients[p.HostIdx()] = &receiptEventClient{HostClient: inner, response: response}
			notified := false
			_, err = s.SendOnly(context.Background(), p, nil, func() { notified = true })
			require.NoError(t, err)
			require.Equal(t, mode == "valid" || mode == "canonical_duplicate", notified)
			if mode == "canonical_duplicate" {
				require.Empty(t, pendingConfirmStarts(s))
			}
		})
	}
}
