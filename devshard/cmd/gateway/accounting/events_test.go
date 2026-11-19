package accounting

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// tickingBook builds a Book whose clock advances one second per reading.
func tickingBook(t *testing.T, groupSize int) *Book {
	t.Helper()
	tick := time.Unix(0, 0).UTC()
	book := NewBook(func() time.Time {
		tick = tick.Add(time.Second)
		return tick
	})
	openTestEscrow(t, book, testEscrow, testEpoch, groupSize)
	return book
}

func decodeEvents(t *testing.T, recorder *httptest.ResponseRecorder) []ProtocolEventRecord {
	t.Helper()
	var body struct {
		Events []ProtocolEventRecord `json:"events"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %s: %v", recorder.Body.String(), err)
	}
	return body.Events
}

// Test flow:
//  1. Record a race on nonce 3 tagged with request id "req-a", then apply a timeout on that nonce.
//  2. Read the events feed.
//  3. Assert it holds exactly one applied-timeout event.
//  4. Assert that event carries the escrow, participant, nonce, slot, kind and request id.
func TestAVerdictNamesTheNonceAndTheRequestThatSpentIt(t *testing.T) {
	book := tickingBook(t, 2)
	if err := book.RecordRace(testEscrow, []Attempt{{Nonce: 3, RequestID: "req-a", Sent: true}}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	if err := book.RecordAppliedTimeout(testEscrow, 3); err != nil {
		t.Fatalf("RecordAppliedTimeout(): %v", err)
	}

	events := book.Events(QueryFilter{})

	if len(events) != 1 {
		t.Fatalf("got %d events, want the one applied timeout", len(events))
	}
	want := ProtocolEventRecord{
		EscrowID: testEscrow, Participant: participantFor(1), Model: testModel,
		Nonce: 3, SlotID: 1, Kind: ProtocolTimeoutApplied, RequestID: "req-a", At: events[0].At,
	}
	if events[0] != want {
		t.Errorf("event = %+v, want %+v", events[0], want)
	}
}

// Test flow:
//  1. Record an invalid verdict on nonce 2.
//  2. Read the events feed.
//  3. Assert it holds exactly one event.
//  4. Assert the event's kind is `ProtocolInvalidated` and its participant is nonce 2's executor.
func TestAnInvalidVerdictReachesTheFeedUnderItsOwnKind(t *testing.T) {
	book := tickingBook(t, 2)
	if err := book.RecordInvalidVerdict(testEscrow, 2); err != nil {
		t.Fatalf("RecordInvalidVerdict(): %v", err)
	}

	events := book.Events(QueryFilter{})

	if len(events) != 1 {
		t.Fatalf("got %d events, want the one invalid verdict", len(events))
	}
	if events[0].Kind != ProtocolInvalidated {
		t.Errorf("kind = %q, want %q", events[0].Kind, ProtocolInvalidated)
	}
	if events[0].Participant != participantFor(0) {
		t.Errorf("charged %s, want the executor of nonce 2", events[0].Participant)
	}
}

// Test flow:
//  1. Apply timeouts on nonces 2, 4 and 6, in that order.
//  2. Read the events feed.
//  3. Assert the nonces come back newest first: 6, 4, 2.
func TestTheFeedReadsNewestFirst(t *testing.T) {
	book := tickingBook(t, 2)
	for _, nonce := range []uint64{2, 4, 6} {
		if err := book.RecordAppliedTimeout(testEscrow, nonce); err != nil {
			t.Fatalf("RecordAppliedTimeout(%d): %v", nonce, err)
		}
	}

	events := book.Events(QueryFilter{})

	got := []uint64{events[0].Nonce, events[1].Nonce, events[2].Nonce}
	if want := []uint64{6, 4, 2}; got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("nonces = %v, want %v", got, want)
	}
}

// Test flow:
//  1. Apply timeouts on 10 more nonces than the ring holds, `maxEventsPerEscrow`.
//  2. Read the events feed.
//  3. Assert the feed's length equals the ring's capacity.
//  4. Assert the newest event is the last nonce recorded and the oldest is the first the ring still holds.
func TestTheRingKeepsTheNewestVerdictsOnly(t *testing.T) {
	book := tickingBook(t, 2)
	for nonce := range uint64(maxEventsPerEscrow + 10) {
		if err := book.RecordAppliedTimeout(testEscrow, nonce); err != nil {
			t.Fatalf("RecordAppliedTimeout(%d): %v", nonce, err)
		}
	}

	events := book.Events(QueryFilter{})

	if len(events) != maxEventsPerEscrow {
		t.Fatalf("feed holds %d events, want the ring's %d", len(events), maxEventsPerEscrow)
	}
	if newest := events[0].Nonce; newest != maxEventsPerEscrow+9 {
		t.Errorf("newest nonce = %d, want the last one recorded", newest)
	}
	if oldest := events[len(events)-1].Nonce; oldest != 10 {
		t.Errorf("oldest nonce = %d, want the first the ring still holds", oldest)
	}
}

// Test flow:
//  1. Apply timeouts on nonce 2, which lands on slot 0, and nonce 3, which lands on slot 1.
//  2. Read the events feed filtered to the slot-1 participant.
//  3. Assert it holds exactly the one event for nonce 3.
func TestTheFeedAnswersOnlyAboutTheHostAsked(t *testing.T) {
	book := tickingBook(t, 2)
	if err := book.RecordAppliedTimeout(testEscrow, 2); err != nil {
		t.Fatalf("RecordAppliedTimeout(): %v", err)
	}
	if err := book.RecordAppliedTimeout(testEscrow, 3); err != nil {
		t.Fatalf("RecordAppliedTimeout(): %v", err)
	}

	events := book.Events(QueryFilter{Participant: participantFor(1)})

	if len(events) != 1 || events[0].Nonce != 3 {
		t.Fatalf("got %+v, want only the nonce that landed on slot 1", events)
	}
}

// Test flow:
//  1. Apply a timeout on nonce 3.
//  2. For both the query-parameter route and the path-segment route to the slot-1 participant's events, serve the request.
//  3. Assert each route answers 200 with exactly the one event for nonce 3.
func TestBothEventRoutesAnswerTheSameFeed(t *testing.T) {
	book := tickingBook(t, 2)
	if err := book.RecordAppliedTimeout(testEscrow, 3); err != nil {
		t.Fatalf("RecordAppliedTimeout(): %v", err)
	}

	for _, target := range []string{
		"/api/v1/epochs/current/events?participant=" + participantFor(1),
		"/api/v1/epochs/current/events/" + participantFor(1),
	} {
		t.Run(target, func(t *testing.T) {
			recorder := serve(t, book, target)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
			}
			events := decodeEvents(t, recorder)
			if len(events) != 1 || events[0].Nonce != 3 {
				t.Fatalf("got %+v, want the one verdict against that host", events)
			}
		})
	}
}

// Test flow:
//  1. Serve a request for the events feed of a participant with no recorded verdicts.
//  2. Assert the response status is 200.
//  3. Assert the decoded feed is empty rather than missing.
func TestAHostWithNoVerdictsGetsAnEmptyFeed(t *testing.T) {
	book := tickingBook(t, 2)

	recorder := serve(t, book, "/api/v1/epochs/current/events/"+participantFor(0))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	if events := decodeEvents(t, recorder); len(events) != 0 {
		t.Errorf("got %+v, want an empty feed", events)
	}
}
