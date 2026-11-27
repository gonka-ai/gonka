package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/registry"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/user"
)

const journalTestEscrow = "journal-escrow"

type recordingJournal struct {
	mu      sync.Mutex
	entries map[uint64]user.InferenceCompletion
	failure error
}

func newRecordingJournal() *recordingJournal {
	return &recordingJournal{entries: map[uint64]user.InferenceCompletion{}}
}

func (j *recordingJournal) SaveInferenceCompletion(_ string, entry user.InferenceCompletion) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failure != nil {
		return j.failure
	}
	j.entries[entry.Nonce] = entry
	return nil
}

func (j *recordingJournal) ListInferenceCompletions(string) ([]user.InferenceCompletion, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	listed := make([]user.InferenceCompletion, 0, len(j.entries))
	for _, entry := range j.entries {
		listed = append(listed, entry)
	}
	return listed, nil
}

func (j *recordingJournal) DeleteInferenceCompletion(_ string, nonce uint64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.entries, nonce)
	return nil
}

func (j *recordingJournal) HasInferenceCompletion(_ string, nonce uint64) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	_, present := j.entries[nonce]
	return present, nil
}

func newJournalTestSession(t *testing.T) (registry.EscrowSession, *user.Session) {
	t.Helper()
	const hosts = 3
	signers := make([]*signing.Secp256k1Signer, hosts)
	for index := range signers {
		signers[index] = testutil.MustGenerateKey(t)
	}
	group := testutil.MakeGroup(signers)
	configuration := testutil.DefaultConfig(hosts)
	creator := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()
	clients := make([]user.HostClient, hosts)
	for index, signer := range signers {
		hostMachine := statetest.MustStateMachine(t, journalTestEscrow, configuration, group, 1<<50, creator.Address(), verifier)
		hostNode, err := host.NewHost(hostMachine, signer, stub.NewInferenceEngine(), journalTestEscrow, group, nil, host.WithGrace(100))
		require.NoError(t, err)
		clients[index] = &user.InProcessClient{Host: hostNode}
	}
	sessionStore, err := storage.NewSQLite(filepath.Join(t.TempDir(), journalTestEscrow))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessionStore.Close()) })
	require.NoError(t, sessionStore.CreateSession(storage.CreateSessionParams{
		EscrowID: journalTestEscrow, Version: testutil.RuntimeTestVersion, CreatorAddr: creator.Address(),
		Config: configuration, Group: group, InitialBalance: 1 << 50,
	}))
	machine, err := state.NewStateMachine(journalTestEscrow, configuration, group, 1<<50, creator.Address(), verifier, sessionStore)
	require.NoError(t, err)
	session, err := user.NewSession(machine, creator, journalTestEscrow, group, clients, verifier, user.WithStorage(sessionStore))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, session.Close()) })
	return registry.NewSessionHandle(session, machine), session
}

func journalTestParams() user.InferenceParams {
	return user.InferenceParams{Model: "llama", Prompt: testutil.TestPrompt, InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}
}

func opening(session registry.EscrowSession) registry.SessionFactory {
	return func(context.Context, string) (registry.EscrowSession, error) { return session, nil }
}

// Test flow:
//  1. Wrap a serving factory that returns a real session with a recording journal.
//  2. Open the session through the wrapper and prepare its first inference.
//  3. Assert the journal holds the started nonce and the session composed it.
func TestAServingSessionJournalsItsFirstStartedNonce(t *testing.T) {
	handle, session := newJournalTestSession(t)
	journal := newRecordingJournal()
	opened, err := journaledSessions(opening(handle), journal)(t.Context(), journalTestEscrow)
	require.NoError(t, err)
	require.NotNil(t, opened)

	_, err = session.PrepareInference(t.Context(), journalTestParams())
	require.NoError(t, err)

	present, err := journal.HasInferenceCompletion(journalTestEscrow, 1)
	require.NoError(t, err)
	require.True(t, present, "HasInferenceCompletion(1) = false, want true")
	require.Equal(t, uint64(1), session.Nonce())
}

// Test flow:
//  1. Wrap a serving factory with a journal whose save fails.
//  2. Prepare the first inference on the opened session.
//  3. Assert the start is refused and no nonce was composed.
func TestAServingSessionComposesNothingWhenTheJournalRefusesTheNonce(t *testing.T) {
	handle, session := newJournalTestSession(t)
	journal := newRecordingJournal()
	journal.failure = errors.New("journal disk full")
	_, err := journaledSessions(opening(handle), journal)(t.Context(), journalTestEscrow)
	require.NoError(t, err)

	_, err = session.PrepareInference(t.Context(), journalTestParams())

	require.ErrorContains(t, err, "journal disk full")
	require.Zero(t, session.Nonce())
	require.Empty(t, session.Diffs())
}

// Test flow:
//  1. Wrap a serving factory that fails with a sentinel error.
//  2. Open a session through the wrapper.
//  3. Assert the same error comes back and no session does.
func TestTheJournalWrapperPassesAFactoryErrorThrough(t *testing.T) {
	sentinel := errors.New("no such escrow")
	failing := func(context.Context, string) (registry.EscrowSession, error) { return nil, sentinel }

	opened, err := journaledSessions(failing, newRecordingJournal())(t.Context(), journalTestEscrow)

	require.Same(t, sentinel, err)
	require.Nil(t, opened)
}

// Test flow:
//  1. Journal nonce 1 for a session that has no journal attached yet, as a previous process left it.
//  2. Let a heartbeat drain take nonce 1 before the wrapper attaches the journal.
//  3. Attach the journal and finalize.
//  4. Assert Finalize succeeds and the journal row at nonce 1 is cleared.
func TestAJournalRowAHeartbeatTookIsClearedAtFinalize(t *testing.T) {
	handle, session := newJournalTestSession(t)
	journal := newRecordingJournal()
	require.NoError(t, journal.SaveInferenceCompletion(journalTestEscrow, user.InferenceCompletion{Nonce: 1}))
	require.NoError(t, session.SendPendingDiff(t.Context()))
	require.Equal(t, uint64(1), session.Nonce())

	_, err := journaledSessions(opening(handle), journal)(t.Context(), journalTestEscrow)
	require.NoError(t, err)
	require.NoError(t, handle.Finalize(t.Context()))

	present, err := journal.HasInferenceCompletion(journalTestEscrow, 1)
	require.NoError(t, err)
	require.False(t, present, "HasInferenceCompletion(1) = true, want false")
}
