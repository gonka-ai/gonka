package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"devshard/cmd/gateway/registry"
	"devshard/types"
)

// sealedRecordSession reports one record the escrow has already sealed out of its live state.
type sealedRecordSession struct {
	registry.EscrowSession
	sealedNonce uint64
}

func (s sealedRecordSession) AllInferences() map[uint64]types.InferenceRecord {
	records := s.EscrowSession.AllInferences()
	if records == nil {
		records = map[uint64]types.InferenceRecord{}
	}
	records[s.sealedNonce] = types.InferenceRecord{Status: types.StatusFinished, ActualCost: 42}
	return records
}

// Test flow:
//  1. Start a harness whose escrow has one record sealed out of its live state.
//  2. Read the escrow's debug inferences with the admin key.
//  3. Assert the sealed record is listed with its status and cost: a settled escrow seals every record, and a view of the live ones alone reads as an escrow that served nothing.
func TestDebugInferencesListsRecordsAlreadySealed(t *testing.T) {
	live := newHarness(t)
	session, machine := newLiveSession(t)
	live.escrows.sessions[liveEscrowID] = sealedRecordSession{EscrowSession: registry.NewSessionHandle(session, machine), sealedNonce: 900}
	request := httptest.NewRequest(http.MethodGet, "/devshard/"+liveEscrowID+"/v1/debug/inferences", nil)
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()

	live.server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("debug inferences: got %d %s, want 200", recorder.Code, recorder.Body.String())
	}
	var answer struct {
		Inferences []struct {
			Nonce  uint64                `json:"nonce"`
			Status string                `json:"status"`
			Record types.InferenceRecord `json:"record"`
		} `json:"inferences"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decoding %s: %v", recorder.Body.String(), err)
	}
	for _, inference := range answer.Inferences {
		if inference.Nonce == 900 {
			if inference.Status != "finished" || inference.Record.ActualCost != 42 {
				t.Fatalf("sealed record = %+v, want finished at cost 42", inference)
			}
			return
		}
	}
	t.Fatalf("debug inferences = %s, want the sealed record 900 listed", recorder.Body.String())
}
