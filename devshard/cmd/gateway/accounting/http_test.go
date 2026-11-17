package accounting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func serve(t *testing.T, book *Book, target string) *httptest.ResponseRecorder {
	t.Helper()
	currentEpoch := func(context.Context) (uint64, error) { return testEpoch, nil }
	recorder := httptest.NewRecorder()
	NewHandler(book, currentEpoch, nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder
}

func decodeParticipants(t *testing.T, recorder *httptest.ResponseRecorder) []ParticipantRecord {
	t.Helper()
	var body struct {
		Participants []ParticipantRecord `json:"participants"`
		Records      []ParticipantRecord `json:"records"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %s: %v", recorder.Body.String(), err)
	}
	return append(body.Participants, body.Records...)
}

// Test flow:
//  1. Build a `twoEpochBook` fixture and request the current-epoch route for one participant.
//  2. Assert the response status is 200.
//  3. Assert exactly one record comes back, for that participant in the current epoch.
func TestParticipantRouteAnswersOnlyAboutThatParticipant(t *testing.T) {
	book := twoEpochBook(t)

	recorder := serve(t, book, "/api/v1/epochs/current/participants/"+participantFor(0))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	records := decodeParticipants(t, recorder)
	if len(records) != 1 {
		t.Fatalf("got %d records, want the one participant of the current epoch", len(records))
	}
	if records[0].Participant != participantFor(0) || records[0].EpochIndex != testEpoch {
		t.Fatalf("got %s in epoch %d, want %s in epoch %d",
			records[0].Participant, records[0].EpochIndex, participantFor(0), testEpoch)
	}
}

// Test flow:
//  1. Build a `twoEpochBook` fixture and request the current-epoch route for an address the ledger never recorded.
//  2. Assert the response status is 404.
func TestParticipantRouteReportsAnAddressTheEpochNeverSaw(t *testing.T) {
	book := twoEpochBook(t)

	recorder := serve(t, book, "/api/v1/epochs/current/participants/gonka1nobody")

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an address with no record", recorder.Code)
	}
}

// Test flow:
//  1. Build a `twoEpochBook` fixture and request the participants route for epoch 0.
//  2. Assert the response status is 400, since epoch 0 would otherwise be read as "every epoch".
func TestEpochZeroIsRefused(t *testing.T) {
	book := twoEpochBook(t)

	recorder := serve(t, book, "/api/v1/epochs/0/participants")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body)
	}
}

// Test flow:
//  1. Build a `twoEpochBook` fixture and request the participants route for epoch 8.
//  2. Assert the response holds at least one record.
//  3. Assert every returned record's epoch index is 8.
func TestParticipantsRouteNarrowsToItsEpoch(t *testing.T) {
	book := twoEpochBook(t)

	recorder := serve(t, book, "/api/v1/epochs/8/participants")

	records := decodeParticipants(t, recorder)
	if len(records) == 0 {
		t.Fatal("epoch 8 came back empty though the ledger holds it")
	}
	for _, record := range records {
		if record.EpochIndex != 8 {
			t.Fatalf("epoch %d present under the route for epoch 8", record.EpochIndex)
		}
	}
}

// Test flow:
//  1. Build a `twoEpochBook` fixture and request the epochs listing route.
//  2. Decode the response body.
//  3. Assert it lists both epochs the ledger holds.
func TestEpochsRouteListsEveryEpochTheLedgerHolds(t *testing.T) {
	book := twoEpochBook(t)

	recorder := serve(t, book, "/api/v1/epochs")

	var body struct {
		Epochs []EpochSummary `json:"epochs"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %s: %v", recorder.Body.String(), err)
	}
	if len(body.Epochs) != 2 {
		t.Fatalf("got %d epochs, want both the ledger holds", len(body.Epochs))
	}
}

// Test flow:
//  1. Build a `troubledBook` fixture and request the current-epoch route for its participant.
//  2. Assert exactly one record comes back.
//  3. Assert that record's findings carry `FindingExecutionTimeouts`.
func TestParticipantRouteCarriesFindings(t *testing.T) {
	recorder := serve(t, troubledBook(t), "/api/v1/epochs/current/participants/"+participantFor(0))

	records := decodeParticipants(t, recorder)
	if len(records) != 1 {
		t.Fatalf("got %d records, want the participant asked about", len(records))
	}
	findingWithCode(t, records[0].Findings, FindingExecutionTimeouts)
}

// Test flow:
//  1. Build a `twoEpochBook` fixture and request a path the epoch selector replaced.
//  2. Assert the response status is 404.
func TestUnknownPathIsRefused(t *testing.T) {
	book := twoEpochBook(t)

	recorder := serve(t, book, "/api/v1/participants")

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for the route the epoch selector replaced", recorder.Code)
	}
}
