package nonces

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
	"devshard/user"
)

type severedHost struct{}

func (severedHost) Send(context.Context, host.HostRequest, io.Writer, func(*host.HostResponse)) (*host.HostResponse, error) {
	return nil, errors.New("the gateway process that owned this session exited")
}

type composedNonces struct{ nonces []uint64 }

func (journal *composedNonces) DiffComposed(_ string, diff *types.Diff) {
	journal.nonces = append(journal.nonces, diff.Nonce)
}

func severedSession(t *testing.T, escrowID string) (*user.Session, []types.SlotAssignment) {
	t.Helper()
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)})
	creator, verifier := testutil.MustGenerateKey(t), signing.NewSecp256k1Verifier()
	machine := statetest.MustStateMachine(t, escrowID, testutil.DefaultConfig(len(group)), group, 100_000, creator.Address(), verifier)
	clients := []user.HostClient{severedHost{}, severedHost{}, severedHost{}}
	session, err := user.NewSession(machine, creator, escrowID, group, clients, verifier)
	if err != nil {
		t.Fatalf("NewSession() = %v, want nil", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, group
}

func restoredRecorder(t *testing.T, escrowID string, group []types.SlotAssignment, latestNonce uint64) *Recorder {
	t.Helper()
	book := accounting.NewBook(func() time.Time { return time.Time{} })
	err := book.Restore(accounting.Snapshot{SchemaVersion: accounting.SchemaVersion, Escrows: []accounting.EscrowSnapshot{
		{Metadata: accounting.EscrowMetadata{EscrowID: escrowID, Model: "llama", Slots: group}, LatestNonce: latestNonce},
	}})
	if err != nil {
		t.Fatalf("Restore() = %v, want nil", err)
	}
	return recorderFor(&accounting.Service{Book: book})
}

// Test flow:
//  1. Compose three service diffs on a session whose hosts are all gone, as the abandoned work of a shutdown composes them after the journal closed.
//  2. Restore a ledger that took the first of them, as the last run saved it, and watch the session's diffs for the first time.
//  3. Assert the journal is handed the two diffs the ledger never took, and nothing it already holds.
//  4. Watch the same session again and assert no diff is handed over twice.
func TestTheFirstWatchHandsOverTheDiffsTheLastRunNeverJournaled(t *testing.T) {
	const escrowID = "escrow-1"
	session, group := severedSession(t, escrowID)
	for range 3 {
		if err := session.SendPendingDiff(context.Background()); err == nil {
			t.Fatal("SendPendingDiff() = nil, want the severed hosts to refuse it")
		}
	}
	retained := session.Diffs()
	if len(retained) != 3 {
		t.Fatalf("session retains %d diffs, want 3", len(retained))
	}
	recorder, journal := restoredRecorder(t, escrowID, group, retained[0].Nonce), &composedNonces{}

	recorder.watchDiffs(escrowID, watchedSession{session: session}, journal)
	want := []uint64{retained[1].Nonce, retained[2].Nonce}
	if !slices.Equal(journal.nonces, want) {
		t.Fatalf("journal took diffs %v, want %v", journal.nonces, want)
	}

	recorder.observing.Delete(escrowID)
	recorder.watchDiffs(escrowID, watchedSession{session: session}, journal)
	if !slices.Equal(journal.nonces, want) {
		t.Fatalf("journal took diffs %v after a second watch, want %v", journal.nonces, want)
	}
}

// Test flow:
//  1. Restore a ledger that took one diff, and watch a session whose next diff is composed while the watch reads the retained ones.
//  2. Assert that diff reaches the journal once, through the observer, and the replay hands over only the service diffs composed before it: a diff that started an inference is classified by its race, not by its diff.
func TestTheReplayStopsAtTheFirstDiffTheObserverSaw(t *testing.T) {
	const escrowID = "escrow-1"
	startedInference := types.Diff{Nonce: 6, Txs: []*types.DevshardTx{{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{}}}}}
	source := &composingSource{diffs: []types.Diff{{Nonce: 4}, {Nonce: 5}, startedInference, {Nonce: 7}}, composedDuringRead: types.Diff{Nonce: 8}}
	recorder, journal := restoredRecorder(t, escrowID, testutil.MakeGroup([]*signing.Secp256k1Signer{testutil.MustGenerateKey(t)}), 4), &composedNonces{}

	recorder.watchSessionDiffs(escrowID, source, journal)

	if want := []uint64{8, 5, 7}; !slices.Equal(journal.nonces, want) {
		t.Fatalf("journal took diffs %v, want %v", journal.nonces, want)
	}
}

type composingSource struct {
	diffs              []types.Diff
	composedDuringRead types.Diff
	observer           func(types.Diff)
}

func (source *composingSource) SetDiffObserver(observer func(types.Diff)) { source.observer = observer }

func (source *composingSource) Diffs() []types.Diff {
	source.observer(source.composedDuringRead)
	return append(source.diffs, source.composedDuringRead)
}
