package registry

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/stub"
	"devshard/types"
	"devshard/user"
)

const forcedFinalizeEscrow = "escrow-forced"

type memoryCompletions struct {
	mu      sync.Mutex
	entries map[uint64]user.InferenceCompletion
}

func (m *memoryCompletions) SaveInferenceCompletion(_ string, entry user.InferenceCompletion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[entry.Nonce] = entry
	return nil
}

func (m *memoryCompletions) ListInferenceCompletions(string) ([]user.InferenceCompletion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	listed := make([]user.InferenceCompletion, 0, len(m.entries))
	for _, entry := range m.entries {
		listed = append(listed, entry)
	}
	return listed, nil
}

func (m *memoryCompletions) DeleteInferenceCompletion(_ string, nonce uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, nonce)
	return nil
}

func (m *memoryCompletions) nonces() []uint64 {
	listed, _ := m.ListInferenceCompletions(forcedFinalizeEscrow)
	nonces := make([]uint64, 0, len(listed))
	for _, entry := range listed {
		nonces = append(nonces, entry.Nonce)
	}
	return nonces
}

func serving(session EscrowSession) SessionFactory {
	return func(context.Context, string) (EscrowSession, error) { return session, nil }
}

func journaledStartedNonce(t *testing.T) (EscrowSession, *memoryCompletions) {
	t.Helper()
	hostSigners := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	group := testutil.MakeGroup(hostSigners)
	config := testutil.DefaultConfig(len(group))
	creator := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()
	clients := make([]user.HostClient, len(hostSigners))
	for index, signer := range hostSigners {
		machine := statetest.MustStateMachine(t, forcedFinalizeEscrow, config, group, 100_000, creator.Address(), verifier)
		hostNode, err := host.NewHost(machine, signer, stub.NewInferenceEngine(), forcedFinalizeEscrow, group, nil, host.WithGrace(100))
		require.NoError(t, err)
		clients[index] = &user.InProcessClient{Host: hostNode}
	}
	machine := statetest.MustStateMachine(t, forcedFinalizeEscrow, config, group, 100_000, creator.Address(), verifier)
	session, err := user.NewSession(machine, creator, forcedFinalizeEscrow, group, clients, verifier)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	completions := &memoryCompletions{entries: map[uint64]user.InferenceCompletion{}}
	session.SetInferenceCompletionStore(completions)
	handle := NewSessionHandle(session, machine)
	_, err = session.PrepareInference(context.Background(), user.InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt, InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)
	require.NoError(t, session.SendPendingDiff(context.Background()))
	require.Equal(t, []uint64{1}, completions.nonces(), "the started nonce must be journaled before the fixture is usable")
	return handle, completions
}

// Test flow:
//  1. Build a session with one started nonce journaled and a registry that serves it.
//  2. Finalize it with the forced path.
//  3. Assert the finalize lands in settlement, reports the unresolved nonce and clears the journal.
func TestAForcedFinalizeSettlesPastAStartedNonceAndClearsTheJournal(t *testing.T) {
	handle, completions := journaledStartedNonce(t)
	registry := New(Deps{ServingSessions: serving(handle), Completions: completions, Now: fixedClock()})
	registry.ShareChainMaxNonce(1_700)

	unresolved, err := registry.FinalizeForced(context.Background(), forcedFinalizeEscrow)

	require.NoError(t, err)
	require.Equal(t, []uint64{1}, unresolved)
	require.Equal(t, types.PhaseSettlement, handle.Phase())
	require.Empty(t, completions.nonces())
}

type wrappedSession struct{ EscrowSession }

// Test flow:
//  1. Build a session with one started nonce journaled and serve it behind a wrapper that embeds the session interface.
//  2. Finalize it with the forced path.
//  3. Assert the finalize lands in settlement, reports the unresolved nonce and clears the journal.
func TestAForcedFinalizeReachesTheSessionBehindAWrapper(t *testing.T) {
	handle, completions := journaledStartedNonce(t)
	registry := New(Deps{ServingSessions: serving(wrappedSession{handle}), Completions: completions, Now: fixedClock()})
	registry.ShareChainMaxNonce(1_700)

	unresolved, err := registry.FinalizeForced(context.Background(), forcedFinalizeEscrow)

	require.NoError(t, err)
	require.Equal(t, []uint64{1}, unresolved)
	require.Equal(t, types.PhaseSettlement, handle.Phase())
	require.Empty(t, completions.nonces())
}

// Test flow:
//  1. Build a session with one started nonce journaled and a registry that holds no journal.
//  2. Finalize it with the forced path.
//  3. Assert the finalize lands in settlement and reports no unresolved nonce.
func TestAForcedFinalizeWithoutAJournalReportsNothing(t *testing.T) {
	handle, completions := journaledStartedNonce(t)
	handle.UserSession().SetInferenceCompletionStore(nil)
	registry := New(Deps{ServingSessions: serving(handle), Now: fixedClock()})
	registry.ShareChainMaxNonce(1_700)

	unresolved, err := registry.FinalizeForced(context.Background(), forcedFinalizeEscrow)

	require.NoError(t, err)
	require.Empty(t, unresolved)
	require.Equal(t, types.PhaseSettlement, handle.Phase())
	require.Equal(t, []uint64{1}, completions.nonces())
}

// Test flow:
//  1. Build a session with one started nonce journaled, finalize it with the forced path and put the cleared row back.
//  2. Finalize it with the forced path again.
//  3. Assert the repeat reports no unresolved nonce and clears the leftover row.
func TestARepeatedForcedFinalizeDoesNotCountTheSameNonceTwice(t *testing.T) {
	handle, completions := journaledStartedNonce(t)
	registry := New(Deps{ServingSessions: serving(handle), Completions: completions, Now: fixedClock()})
	registry.ShareChainMaxNonce(1_700)
	first, err := registry.FinalizeForced(context.Background(), forcedFinalizeEscrow)
	require.NoError(t, err)
	require.Equal(t, []uint64{1}, first)
	require.NoError(t, completions.SaveInferenceCompletion(forcedFinalizeEscrow, user.InferenceCompletion{Nonce: 1}))

	second, err := registry.FinalizeForced(context.Background(), forcedFinalizeEscrow)

	require.NoError(t, err)
	require.Empty(t, second)
	require.Empty(t, completions.nonces())
}

type failingFinalizeSession struct {
	EscrowSession
	err error
}

func (s failingFinalizeSession) Finalize(context.Context) error { return s.err }

// Test flow:
//  1. Build a session with one started nonce journaled and serve it behind a wrapper whose finalize fails.
//  2. Finalize it with the forced path.
//  3. Assert the failure is returned with no unresolved nonce, the journal row stays and the next nonce is journaled again.
func TestAFailedForcedFinalizeKeepsTheJournalRowsAndReattachesTheJournal(t *testing.T) {
	handle, completions := journaledStartedNonce(t)
	failure := errors.New("hosts unreachable")
	registry := New(Deps{ServingSessions: serving(failingFinalizeSession{handle, failure}), Completions: completions, Now: fixedClock()})
	registry.ShareChainMaxNonce(1_700)

	unresolved, err := registry.FinalizeForced(context.Background(), forcedFinalizeEscrow)

	require.ErrorIs(t, err, failure)
	require.Empty(t, unresolved)
	require.Equal(t, []uint64{1}, completions.nonces())
	_, err = handle.UserSession().PrepareInference(context.Background(), user.InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt, InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)
	require.Len(t, completions.nonces(), 2, "the prepare after the failed finalize must be journaled again")
	require.Contains(t, completions.nonces(), uint64(1))
}

type unreadableCompletions struct {
	*memoryCompletions
	err error
}

func (u unreadableCompletions) ListInferenceCompletions(string) ([]user.InferenceCompletion, error) {
	return nil, u.err
}

// Test flow:
//  1. Build a session with one started nonce and a journal whose reads fail, with a recording narrator.
//  2. Finalize it with the forced path.
//  3. Assert the finalize lands, reports no unresolved nonce and the narrator recorded the journal failure.
func TestAForcedFinalizeNarratesAJournalItCannotRead(t *testing.T) {
	handle, completions := journaledStartedNonce(t)
	failure := errors.New("store down")
	narrator := &recordingEscrowNarrator{}
	registry := New(Deps{
		ServingSessions: serving(handle),
		Completions:     unreadableCompletions{completions, failure},
		Narrator:        narrator,
		Now:             fixedClock(),
	})
	registry.ShareChainMaxNonce(1_700)

	unresolved, err := registry.FinalizeForced(context.Background(), forcedFinalizeEscrow)

	require.NoError(t, err)
	require.Empty(t, unresolved)
	require.Equal(t, types.PhaseSettlement, handle.Phase())
	require.Contains(t, narrator.recorded(), "completion journal failed "+forcedFinalizeEscrow+" error "+failure.Error())
}
