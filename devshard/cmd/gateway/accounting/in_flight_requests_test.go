package accounting

import (
	"testing"

	"devshard/cmd/gateway/engine"
)

// Test flow:
//  1. Open a book and observe the chain's latest nonce as 8.
//  2. Assign nonces 4 and 8, both landing on slot 0 and both belonging to request "req-a".
//  3. Assert the slot-0 record reports 2 nonces in flight.
//  4. Assert it reports 1 in-flight request, since both nonces belong to the same request.
func TestOpenNoncesAndOpenRequestsAreCountedApart(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.ObserveLatestNonce(testEscrow, 8); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}
	for _, nonce := range []uint64{4, 8} {
		if err := book.RecordAssigned(testEscrow, nonce, "req-a"); err != nil {
			t.Fatalf("RecordAssigned(%d): %v", nonce, err)
		}
	}

	records := book.Query(QueryFilter{Participant: participantFor(0)})

	if len(records) != 1 {
		t.Fatalf("got %d participant records, want one", len(records))
	}
	if records[0].InFlight != 2 {
		t.Fatalf("in flight = %d, want the 2 nonces still open", records[0].InFlight)
	}
	if records[0].InFlightRequests != 1 {
		t.Fatalf("in-flight requests = %d, want the 1 request the two nonces belong to", records[0].InFlightRequests)
	}
}

// Test flow:
//  1. Assign nonces 4 and 8 to request "req-a" and assert 2 nonces and 1 request in flight.
//  2. Record the race: nonce 4 won and its finish is applied, nonce 8 lost and is still unfinished.
//  3. Assert the loser nonce stays in flight while the request is closed: the client has its answer.
//  4. Mark nonce 8 finished and assert nothing is in flight.
func TestARequestClosesWhenItsRaceReportsWhileALoserNonceStaysOpen(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.ObserveLatestNonce(testEscrow, 8); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}
	for _, nonce := range []uint64{4, 8} {
		if err := book.RecordAssigned(testEscrow, nonce, "req-a"); err != nil {
			t.Fatalf("RecordAssigned(%d): %v", nonce, err)
		}
	}
	racing := book.Query(QueryFilter{Participant: participantFor(0)})
	if racing[0].InFlight != 2 || racing[0].InFlightRequests != 1 {
		t.Fatalf("open = %d nonces / %d requests while racing, want 2 and 1",
			racing[0].InFlight, racing[0].InFlightRequests)
	}

	if err := book.RecordRace(testEscrow, []Attempt{
		{Nonce: 4, RequestID: "req-a", Sent: true, Terminal: engine.TerminalNameWon},
		{Nonce: 8, RequestID: "req-a", Sent: true, Terminal: engine.TerminalNameLost},
	}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	markFinished(t, book, testEscrow, 4)

	answered := book.Query(QueryFilter{Participant: participantFor(0)})
	if answered[0].InFlight != 1 || answered[0].InFlightRequests != 0 {
		t.Fatalf("open = %d nonces / %d requests after the race reported, want 1 and 0: the loser waits for its timeout, the client does not",
			answered[0].InFlight, answered[0].InFlightRequests)
	}

	if err := book.MarkFinished(testEscrow, []uint64{8}); err != nil {
		t.Fatalf("MarkFinished(): %v", err)
	}

	settled := book.Query(QueryFilter{Participant: participantFor(0)})
	if settled[0].InFlight != 0 || settled[0].InFlightRequests != 0 {
		t.Fatalf("open = %d nonces / %d requests after the loser settled, want none",
			settled[0].InFlight, settled[0].InFlightRequests)
	}
}
