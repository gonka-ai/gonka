package nonces

import (
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/types"
)

// Test flow:
//  1. Report the races of nonces 4, 5 and 6 as won while the sweep never read any of them live.
//  2. Take three readings against a window that holds none of them: 4 sealed validated, 5 sealed timed out, 6 with no copy kept.
//  3. Assert the nonce sealed timed out was read once, the nonce with no copy on every reading, and the ledger still holds 5 and 6 unfinished.
//  4. Retire the escrow and take one more reading, and assert the nonce sealed timed out is read again.
func TestANonceSealedTimedOutIsReadOnceAcrossReadings(t *testing.T) {
	t.Parallel()
	service, err := accounting.NewService(accounting.Settings{Now: func() time.Time { return time.Unix(0, 0).UTC() }})
	if err != nil {
		t.Fatalf("NewService(): %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	ledger := recorderFor(service)
	escrow := newLiveEscrow(t, 3)
	snapshot := escrow.machine.SnapshotState()
	if err := service.Book.OpenEscrow(accounting.EscrowMetadata{EscrowID: tokenEscrowID, Model: "llama", CreationEpoch: 7, Slots: snapshot.Group}); err != nil {
		t.Fatalf("OpenEscrow(): %v", err)
	}
	for _, nonce := range []uint64{4, 5, 6} {
		wonRace(t, nonce)(service.Book)
	}
	snapshot.LatestNonce = 6
	session := sealedSession{
		sealed:  map[uint64]types.InferenceRecord{4: {Status: types.StatusValidated}, 5: {Status: types.StatusTimedOut}},
		lookups: make(map[uint64]int),
	}

	for range 3 {
		ledger.closeSealed(tokenEscrowID, snapshot, newSealedRecords(session))
	}

	if got := session.lookups[5]; got != 1 {
		t.Fatalf("lookups of the nonce sealed timed out = %d over three readings, want 1", got)
	}
	if got := session.lookups[6]; got != 3 {
		t.Fatalf("lookups of the nonce with no copy kept = %d over three readings, want 3", got)
	}
	if got := service.Book.UnfinishedNonces(tokenEscrowID); !slices.Equal(got, []uint64{5, 6}) {
		t.Fatalf("UnfinishedNonces() = %v, want [5 6]", got)
	}

	ledger.EscrowRetiring(tokenEscrowID, nil)
	ledger.closeSealed(tokenEscrowID, snapshot, newSealedRecords(session))

	if got := session.lookups[5]; got != 2 {
		t.Fatalf("lookups of the nonce sealed timed out = %d after the escrow retired, want 2", got)
	}
}
