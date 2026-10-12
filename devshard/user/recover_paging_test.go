package user

import (
	"context"
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/types"
)

// wholeJournalPageSize makes every recovery read one page, the way recovery read the journal before paging.
const wholeJournalPageSize uint64 = 1 << 40

// inferenceEveryNonces spaces real inferences through a recovery history so its diffs carry confirms, finishes and signatures.
const inferenceEveryNonces = 25

// snapshotlessStore hides every snapshot, so recovery replays the journal from nonce 1.
type snapshotlessStore struct {
	storage.Storage
}

func (store snapshotlessStore) LoadSnapshot(string) (uint64, []byte, error) {
	return 0, nil, storage.ErrSnapshotNotFound
}

// journalGapStore drops one nonce from every diff read, as a journal missing that diff.
type journalGapStore struct {
	storage.Storage
	missingNonce uint64
}

func (store journalGapStore) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	records, err := store.Storage.GetDiffs(escrowID, fromNonce, toNonce)
	if err != nil {
		return nil, err
	}
	kept := make([]types.DiffRecord, 0, len(records))
	for _, record := range records {
		if record.Nonce != store.missingNonce {
			kept = append(kept, record)
		}
	}
	return kept, nil
}

// recoveryHistory is an escrow's journal and the identities recovery needs to read it back.
type recoveryHistory struct {
	store     *storage.SQLite
	live      *Session
	hostKeys  []*signing.Secp256k1Signer
	userKey   *signing.Secp256k1Signer
	group     []types.SlotAssignment
	liveRoot  []byte
	liveNonce uint64
}

func buildRecoveryHistory(t *testing.T, nonceCount int) recoveryHistory {
	t.Helper()
	store := newTestStore(t)
	live, liveMachine, group, hostKeys, userKey := buildLiveSession(t, 3, store)
	for nonce := 1; nonce <= nonceCount; nonce++ {
		if nonce%inferenceEveryNonces == 1 {
			_, err := live.SendInference(context.Background(), storedCatchUpInference())
			require.NoError(t, err)
			continue
		}
		composeUnsentDiffs(t, live, 1)
	}
	require.Eventually(t, func() bool { return !live.snapshotInFlight.Load() }, 10*time.Second, time.Millisecond)
	liveRoot, err := liveMachine.ComputeStateRoot()
	require.NoError(t, err)
	return recoveryHistory{store: store, live: live, hostKeys: hostKeys, userKey: userKey, group: group, liveRoot: liveRoot, liveNonce: live.Nonce()}
}

// recoveredView is everything recovery restores that paging could change.
type recoveredView struct {
	nonce          uint64
	stateRoot      []byte
	signatures     map[uint64]map[uint32][]byte
	hostSyncNonce  map[int]uint64
	diffNonces     []uint64
	validationRows []storage.SlotValidationObs
}

func recoverWithPageSize(t *testing.T, history recoveryHistory, store storage.Storage, pageSize uint64) (recoveredView, error) {
	t.Helper()
	previousPageSize := diffPageSize
	diffPageSize = pageSize
	defer func() { diffPageSize = previousPageSize }()
	session, machine, err := RecoverSession(store, history.userKey, signing.NewSecp256k1Verifier(), "escrow-1", testutil.RuntimeTestVersion,
		history.group, buildRecoveryClients(t, history.hostKeys, history.group, history.userKey))
	if err != nil {
		return recoveredView{}, err
	}
	stateRoot, err := machine.ComputeStateRoot()
	require.NoError(t, err)
	validationRows, err := history.store.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	session.mu.Lock()
	defer session.mu.Unlock()
	view := recoveredView{
		nonce:          session.nonce,
		stateRoot:      stateRoot,
		signatures:     maps.Clone(session.signatures),
		hostSyncNonce:  maps.Clone(session.hostSyncNonce),
		validationRows: validationRows,
	}
	for _, diff := range session.diffs {
		view.diffNonces = append(view.diffNonces, diff.Nonce)
	}
	return view, nil
}

// Test flow:
//  1. Build histories of 1, 1023, 1024, 1025, 2049 and 5000 nonces, a real inference every 25 of them.
//  2. Recover each without a snapshot, and from a snapshot at its tip with host 0 stranded at nonce 0.
//  3. Recovery in pages of 1024 restores the same nonce, root, signatures, cursors, diffs and validation rows as one whole read.
//  4. The recovered root and nonce are the live session's, and memory holds less than twice the diff window.
func TestRecoverSession_PagedRecoveryMatchesAWholeJournalRead(t *testing.T) {
	for _, nonceCount := range []int{1, 1023, 1024, 1025, 2049, 5000} {
		for _, withSnapshot := range []bool{false, true} {
			t.Run(fmt.Sprintf("nonces=%d/snapshot=%t", nonceCount, withSnapshot), func(t *testing.T) {
				history := buildRecoveryHistory(t, nonceCount)
				var store storage.Storage = snapshotlessStore{Storage: history.store}
				if withSnapshot {
					history.live.mu.Lock()
					history.live.hostSyncNonce[0] = 0
					history.live.mu.Unlock()
					require.NoError(t, history.live.FlushSnapshot())
					store = history.store
				}

				whole, wholeErr := recoverWithPageSize(t, history, store, wholeJournalPageSize)
				paged, pagedErr := recoverWithPageSize(t, history, store, 1024)

				require.NoError(t, wholeErr)
				require.NoError(t, pagedErr)
				require.Equal(t, whole.nonce, paged.nonce, "paged recovery nonce = %d, want %d", paged.nonce, whole.nonce)
				require.Equal(t, whole.stateRoot, paged.stateRoot, "paged recovery root = %x, want %x", paged.stateRoot, whole.stateRoot)
				require.Equal(t, whole.signatures, paged.signatures, "paged recovery signatures differ from the whole-journal recovery")
				require.Equal(t, whole.hostSyncNonce, paged.hostSyncNonce, "paged recovery cursors = %v, want %v", paged.hostSyncNonce, whole.hostSyncNonce)
				require.Equal(t, whole.diffNonces, paged.diffNonces, "paged recovery holds %d diffs, want %d", len(paged.diffNonces), len(whole.diffNonces))
				require.Equal(t, whole.validationRows, paged.validationRows, "paged recovery validation rows = %v, want %v", paged.validationRows, whole.validationRows)
				require.Equal(t, history.liveNonce, paged.nonce)
				require.Equal(t, history.liveRoot, paged.stateRoot, "recovered root = %x, want the live root %x", paged.stateRoot, history.liveRoot)
				require.Less(t, len(paged.diffNonces), 2*defaultDiffsKeptInMemory)
			})
		}
	}
}

// Test flow:
//  1. Build a 2049-nonce history and hide one diff at the first nonce, a page's last, the next page's first, or the tip.
//  2. Recover without a snapshot in pages of 1024 and in one whole read.
//  3. Both refuse with ErrLocalStateUnrecoverable.
func TestRecoverSession_PagedRecoveryRefusesAJournalGapLikeAWholeRead(t *testing.T) {
	history := buildRecoveryHistory(t, 2049)
	for _, missingNonce := range []uint64{1, 1024, 1025, 2049} {
		t.Run(fmt.Sprintf("missing=%d", missingNonce), func(t *testing.T) {
			store := snapshotlessStore{Storage: journalGapStore{Storage: history.store, missingNonce: missingNonce}}

			_, wholeErr := recoverWithPageSize(t, history, store, wholeJournalPageSize)
			_, pagedErr := recoverWithPageSize(t, history, store, 1024)

			require.ErrorIs(t, wholeErr, ErrLocalStateUnrecoverable, "whole-journal recovery = %v, want %v", wholeErr, ErrLocalStateUnrecoverable)
			require.ErrorIs(t, pagedErr, ErrLocalStateUnrecoverable, "paged recovery = %v, want %v", pagedErr, ErrLocalStateUnrecoverable)
		})
	}
}
