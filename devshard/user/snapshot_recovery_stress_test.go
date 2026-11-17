//go:build stress

package user

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

const (
	defaultSnapshotRecoveryStressSessions = 100
	defaultSnapshotRecoveryStressTail     = 100
	defaultSnapshotRecoveryStressHosts    = 3
	snapshotRecoveryStressBalance         = 10_000_000
)

type snapshotRecoveryStressFixture struct {
	escrowID string
	group    []types.SlotAssignment
	hosts    []*signing.Secp256k1Signer
	user     *signing.Secp256k1Signer
	root     []byte
}

type snapshotRecoveryStressGetDiffsStore struct {
	storage.Storage
	mu    sync.Mutex
	calls []snapshotRecoveryStressGetDiffsCall
}

type snapshotRecoveryStressGetDiffsCall struct {
	escrowID string
	from     uint64
	to       uint64
	records  int
}

func (s *snapshotRecoveryStressGetDiffsStore) GetDiffs(escrowID string, from, to uint64) ([]types.DiffRecord, error) {
	recs, err := s.Storage.GetDiffs(escrowID, from, to)
	s.mu.Lock()
	s.calls = append(s.calls, snapshotRecoveryStressGetDiffsCall{
		escrowID: escrowID,
		from:     from,
		to:       to,
		records:  len(recs),
	})
	s.mu.Unlock()
	return recs, err
}

func (s *snapshotRecoveryStressGetDiffsStore) snapshot() []snapshotRecoveryStressGetDiffsCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]snapshotRecoveryStressGetDiffsCall, len(s.calls))
	copy(out, s.calls)
	return out
}

// TestStressSnapshotRecoveryBudget verifies that snapshot-backed recovery keeps
// the replay budget bounded across many active sessions. The current recovery
// path folds the pre-snapshot journal for height sync, scans the full journal
// for applied tx keys, and replays the tail. Budget for those known reads,
// while enforcing the v6 page limit and detecting ignored snapshots.
func TestStressSnapshotRecoveryBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping snapshot recovery stress test in short mode")
	}

	numSessions := stressEnvInt(t, "DEVSHARD_SNAPSHOT_RECOVERY_STRESS_SESSIONS", defaultSnapshotRecoveryStressSessions)
	numHosts := stressEnvInt(t, "DEVSHARD_SNAPSHOT_RECOVERY_STRESS_HOSTS", defaultSnapshotRecoveryStressHosts)
	tail := stressEnvInt(t, "DEVSHARD_SNAPSHOT_RECOVERY_STRESS_TAIL", defaultSnapshotRecoveryStressTail)
	require.Positive(t, numSessions)
	require.Positive(t, numHosts)
	require.Positive(t, tail)

	snapshotNonce := uint64(snapshotInterval)
	latestNonce := snapshotNonce + uint64(tail)
	store := newTestStore(t)

	populateStartedAt := time.Now()
	fixtures := make([]snapshotRecoveryStressFixture, 0, numSessions)
	for i := 0; i < numSessions; i++ {
		escrowID := fmt.Sprintf("snapshot-recovery-stress-%04d", i)
		fixtures = append(fixtures, buildSnapshotRecoveryStressSession(
			t, store, escrowID, numHosts, snapshotNonce, latestNonce,
		))
	}
	t.Logf("populated %d sessions at latest_nonce=%d with snapshots at nonce=%d in %s",
		numSessions, latestNonce, snapshotNonce, time.Since(populateStartedAt))

	spy := &snapshotRecoveryStressGetDiffsStore{Storage: store}
	recoverStartedAt := time.Now()
	verifier := signing.NewSecp256k1Verifier()
	for _, fixture := range fixtures {
		clients := buildSnapshotRecoveryStressClients(t, fixture.escrowID, fixture.group, fixture.hosts, fixture.user, latestNonce)
		recovered, recoveredSM, err := RecoverSession(spy, fixture.user, verifier, fixture.escrowID, testutil.RuntimeTestVersion, fixture.group, clients)
		require.NoError(t, err, "recover %s", fixture.escrowID)
		require.Equal(t, latestNonce, recovered.Nonce(), "recover nonce %s", fixture.escrowID)

		root, err := recoveredSM.ComputeStateRoot()
		require.NoError(t, err)
		require.Equal(t, fixture.root, root, "state root %s", fixture.escrowID)
	}
	recoverDuration := time.Since(recoverStartedAt)

	calls := spy.snapshot()
	totalCalls, totalRecords := snapshotRecoveryStressTotals(calls)
	prefixCalls, prefixRecords := snapshotRecoveryStressRangeTotals(calls, 1, snapshotNonce)
	tailCalls, tailRecords := snapshotRecoveryStressRangeTotals(calls, snapshotNonce+1, latestNonce)

	// v6 pages the height-sync fold, tail replay, and applied-key scan. Count
	// overlapping nonces rather than expecting a single GetDiffs per range;
	// one scan page can straddle the snapshot boundary.
	expectedTailRecords := 2 * numSessions * tail
	maxExpectedBackfillRecords := numSessions * numHosts
	expectedPrefixRecords := 2 * numSessions * int(snapshotNonce)
	maxExpectedRecords := expectedPrefixRecords + expectedTailRecords + maxExpectedBackfillRecords

	t.Logf("recovered %d sessions in %s", numSessions, recoverDuration)
	t.Logf("GetDiffs budget: calls=%d records=%d tail_calls=%d tail_records=%d prefix_calls=%d prefix_records=%d max_expected_records=%d",
		totalCalls, totalRecords, tailCalls, tailRecords, prefixCalls, prefixRecords, maxExpectedRecords)

	for _, call := range calls {
		require.LessOrEqual(t, call.to-call.from+1, uint64(storage.DiffPageMaxNonces),
			"GetDiffs exceeded the v6 nonce page limit for %s", call.escrowID)
		require.Equal(t, int(call.to-call.from+1), call.records, "incomplete journal page for %s", call.escrowID)
	}
	// Check each session independently so an over-read cannot hide behind
	// another session's missing reads.
	for _, fixture := range fixtures {
		var sessionCalls []snapshotRecoveryStressGetDiffsCall
		tailStarts := 0
		for _, call := range calls {
			if call.escrowID == fixture.escrowID {
				sessionCalls = append(sessionCalls, call)
				if call.from == snapshotNonce+1 {
					tailStarts++
				}
			}
		}
		_, prefix := snapshotRecoveryStressRangeTotals(sessionCalls, 1, snapshotNonce)
		_, suffix := snapshotRecoveryStressRangeTotals(sessionCalls, snapshotNonce+1, latestNonce)
		require.Equal(t, 2*tail, suffix, "expected one tail replay and one applied-key scan for %s", fixture.escrowID)
		require.Equal(t, 1, tailStarts, "expected replay to start immediately after the snapshot for %s", fixture.escrowID)
		require.GreaterOrEqual(t, prefix, 2*int(snapshotNonce), "height-sync or applied-key scan missed history for %s", fixture.escrowID)
		require.LessOrEqual(t, prefix, 2*int(snapshotNonce)+numHosts, "snapshot backfill exceeded host cursor lag for %s", fixture.escrowID)
	}
	require.Equal(t, expectedTailRecords, tailRecords)
	require.LessOrEqual(t, totalRecords, maxExpectedRecords, "total GetDiffs budget suggests snapshots were ignored")
}

func buildSnapshotRecoveryStressSession(
	t *testing.T,
	store storage.Storage,
	escrowID string,
	numHosts int,
	snapshotNonce uint64,
	latestNonce uint64,
) snapshotRecoveryStressFixture {
	t.Helper()

	hostSigners := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := snapshotRecoveryStressConfig(numHosts, latestNonce)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       escrowID,
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    userKey.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: snapshotRecoveryStressBalance,
	}))

	clients := buildSnapshotRecoveryStressClients(t, escrowID, group, hostSigners, userKey, latestNonce)
	userStore := testutil.MustMemoryStore(t, escrowID, userKey.Address(), config, group, snapshotRecoveryStressBalance)
	userSM, err := state.NewStateMachine(escrowID, config, group, snapshotRecoveryStressBalance, userKey.Address(), verifier, userStore)
	require.NoError(t, err)
	session, err := NewSession(userSM, userKey, escrowID, group, clients, verifier, WithStorage(store))
	require.NoError(t, err)

	params := InferenceParams{
		Model:       "llama",
		Prompt:      testutil.TestPrompt,
		InputLength: 100,
		MaxTokens:   testutil.TestMaxTokens,
		StartedAt:   1000,
	}
	ctx := context.Background()
	for session.Nonce() < snapshotNonce {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err, "advance %s to snapshot nonce", escrowID)
	}
	require.Equal(t, snapshotNonce, session.Nonce())
	require.NoError(t, session.FlushSnapshot())
	snapNonce, _, err := store.LoadSnapshot(escrowID)
	require.NoError(t, err)
	require.Equal(t, snapshotNonce, snapNonce, "snapshot nonce %s", escrowID)

	for session.Nonce() < latestNonce {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err, "advance %s after snapshot", escrowID)
	}

	root, err := userSM.ComputeStateRoot()
	require.NoError(t, err)

	return snapshotRecoveryStressFixture{
		escrowID: escrowID,
		group:    group,
		hosts:    hostSigners,
		user:     userKey,
		root:     root,
	}
}

func buildSnapshotRecoveryStressClients(
	t *testing.T,
	escrowID string,
	group []types.SlotAssignment,
	hostSigners []*signing.Secp256k1Signer,
	userKey *signing.Secp256k1Signer,
	latestNonce uint64,
) []HostClient {
	t.Helper()

	config := snapshotRecoveryStressConfig(len(hostSigners), latestNonce)
	verifier := signing.NewSecp256k1Verifier()
	clients := make([]HostClient, len(hostSigners))
	for i := range hostSigners {
		hostStore := testutil.MustMemoryStore(t, escrowID, userKey.Address(), config, group, snapshotRecoveryStressBalance)
		sm, err := state.NewStateMachine(escrowID, config, group, snapshotRecoveryStressBalance, userKey.Address(), verifier, hostStore)
		require.NoError(t, err)
		h, err := host.NewHost(sm, hostSigners[i], stub.NewInferenceEngine(), escrowID, group, nil, host.WithGrace(latestNonce+100))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}
	return clients
}

func snapshotRecoveryStressTotals(calls []snapshotRecoveryStressGetDiffsCall) (callCount int, recordCount int) {
	for _, call := range calls {
		callCount++
		recordCount += call.records
	}
	return callCount, recordCount
}

func snapshotRecoveryStressRangeTotals(calls []snapshotRecoveryStressGetDiffsCall, from, to uint64) (callCount int, recordCount int) {
	for _, call := range calls {
		lo, hi := max(call.from, from), min(call.to, to)
		if lo <= hi {
			callCount++
			recordCount += int(hi - lo + 1)
		}
	}
	return callCount, recordCount
}

func snapshotRecoveryStressConfig(numHosts int, latestNonce uint64) types.SessionConfig {
	cfg := testutil.DefaultConfig(numHosts)
	cfg.AutoSealEveryNNonces = uint32(latestNonce + 1)
	return cfg
}

func stressEnvInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	require.NoError(t, err, "parse %s", name)
	return value
}
