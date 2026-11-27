package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"devshard/cmd/gateway/registry"
	"devshard/gatewayprotocol/settlement"
	"devshard/types"
)

type phasedSession struct {
	registry.EscrowSession
	phase         types.SessionPhase
	finalizeNonce uint64
}

func (s phasedSession) SnapshotState() types.EscrowState {
	return types.EscrowState{Phase: s.phase, FinalizeNonce: s.finalizeNonce}
}

// Test flow:
//  1. Register an escrow in each phase, with a settlement the registry can build for it.
//  2. GET its finalize route as admin.
//  3. Assert the active and the finalizing escrow answer 409, and the settled one answers 200 with the settlement file of its message plus the phase and finalize nonce.
func TestTheFinalizeGetServesTheSettlementFileOnlyOnceSettled(t *testing.T) {
	message := settlement.Message{
		EscrowID:       7,
		StateRoot:      []byte("state-root"),
		Nonce:          9,
		RestHash:       []byte("rest-hash"),
		HostStats:      []settlement.HostStat{{SlotID: 1, Missed: 2, Invalid: 3, Cost: 4, RequiredValidations: 5, CompletedValidations: 6, Validated: 7, Finished: 8}},
		SlotSignatures: []settlement.SlotSignature{{SlotID: 1, Signature: []byte("signature")}},
		Fees:           10,
		Version:        "v7",
	}
	for _, testCase := range []struct {
		name  string
		phase types.SessionPhase
		want  int
	}{
		{name: "active", phase: types.PhaseActive, want: http.StatusConflict},
		{name: "finalizing", phase: types.PhaseFinalizing, want: http.StatusConflict},
		{name: "settlement", phase: types.PhaseSettlement, want: http.StatusOK},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t)
			live.escrows.sessions["7"] = phasedSession{phase: testCase.phase, finalizeNonce: 12}
			live.escrows.settlements = map[string]settlement.Message{"7": message}

			recorder := live.request(t, http.MethodGet, "/devshard/7/v1/finalize", "", adminHeaders())

			if recorder.Code != testCase.want {
				t.Fatalf("status = %d (%s), want %d", recorder.Code, recorder.Body.String(), testCase.want)
			}
			if testCase.want != http.StatusOK {
				return
			}
			want, err := json.Marshal(finalizedDevshard{File: settlement.NewFile(message), Phase: "settlement", FinalizeNonce: 12})
			if err != nil {
				t.Fatal(err)
			}
			var wantBody, gotBody map[string]any
			if err := json.Unmarshal(want, &wantBody); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &gotBody); err != nil {
				t.Fatalf("body %s: %v", recorder.Body.String(), err)
			}
			if !reflect.DeepEqual(gotBody, wantBody) {
				t.Fatalf("body = %v, want %v", gotBody, wantBody)
			}
		})
	}
}

// Test flow:
//  1. Register a settled escrow whose settlement the registry cannot build.
//  2. GET its finalize route as admin.
//  3. Assert the answer is 500 and names the failure, not an empty settlement file.
func TestTheFinalizeGetReportsASettlementItCannotBuild(t *testing.T) {
	live := newHarness(t)
	live.escrows.sessions["7"] = phasedSession{phase: types.PhaseSettlement}
	live.escrows.settleErr = errors.New("choosing settlement signatures: below quorum")

	recorder := live.request(t, http.MethodGet, "/devshard/7/v1/finalize", "", adminHeaders())

	if recorder.Code != http.StatusInternalServerError || !json.Valid(recorder.Body.Bytes()) {
		t.Fatalf("status = %d (%s), want a JSON 500", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Body.String(); !strings.Contains(got, "below quorum") {
		t.Fatalf("body = %s, want the failure's text", got)
	}
}

// Test flow:
//  1. Hold a settled escrow only as a parked one, which the registry no longer keeps resident but can reopen read-only, with a settlement it can build.
//  2. GET its finalize route as admin.
//  3. Assert the answer is 200 with the settlement file and the settlement phase, not 404.
func TestTheFinalizeGetServesTheSettlementFileOfAParkedEscrow(t *testing.T) {
	live := newHarness(t)
	live.escrows.parked = map[string]registry.EscrowSession{"7": phasedSession{phase: types.PhaseSettlement, finalizeNonce: 12}}
	live.escrows.settlements = map[string]settlement.Message{"7": {EscrowID: 7, Nonce: 9, Version: "v7"}}

	recorder := live.request(t, http.MethodGet, "/devshard/7/v1/finalize", "", adminHeaders())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want %d", recorder.Code, recorder.Body.String(), http.StatusOK)
	}
	var body finalizedDevshard
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %s: %v", recorder.Body.String(), err)
	}
	if body.EscrowID != "7" || body.Phase != "settlement" || body.FinalizeNonce != 12 {
		t.Fatalf("body = %+v, want escrow 7 in the settlement phase at finalize nonce 12", body)
	}
}
