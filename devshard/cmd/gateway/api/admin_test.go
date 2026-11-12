package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/store"
)

// Test flow:
//  1. Send a DELETE for escrow "7" through the admin route.
//  2. Assert the response is 200 and the operator deleted exactly that escrow.
func TestDeletingADevshardHandsItToTheOperator(t *testing.T) {
	live := newHarness(t)
	recorder := live.request(t, http.MethodDelete, "/v1/admin/devshards/7", "", adminHeaders())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status: got %d (%s)", recorder.Code, recorder.Body.String())
	}
	if !slices.Equal(live.operations.deleted, []string{"7"}) {
		t.Fatalf("deleted escrows: %v, want [7]", live.operations.deleted)
	}
}

// Test flow:
//  1. For each refusal the operator answers a delete with, set it as the operator's error.
//  2. Send a DELETE for escrow "7" through the admin route.
//  3. Assert the response is 409.
func TestARefusedDeleteAnswersConflict(t *testing.T) {
	refusals := map[string]error{
		"busy":   fmt.Errorf("%w: 7", escrow.ErrDevshardBusy),
		"parked": fmt.Errorf("%w: escrow 7 is parked for settlement", escrow.ErrDevshardBusy),
	}
	for name, refusal := range refusals {
		t.Run(name, func(t *testing.T) {
			live := newHarness(t)
			live.operations.err = refusal
			recorder := live.request(t, http.MethodDelete, "/v1/admin/devshards/7", "", adminHeaders())
			if recorder.Code != http.StatusConflict {
				t.Fatalf("status: got %d, want 409", recorder.Code)
			}
		})
	}
}

// Test flow:
//  1. Make the operator answer a delete with ErrUnknownDevshard, as when a settle it waited behind deleted the row.
//  2. Send a DELETE for escrow "7" through the admin route.
//  3. Assert the response is 404.
func TestADeleteOfARowGoneMeanwhileAnswersNotFound(t *testing.T) {
	live := newHarness(t)
	live.operations.err = fmt.Errorf("%w: 7", ErrUnknownDevshard)
	recorder := live.request(t, http.MethodDelete, "/v1/admin/devshards/7", "", adminHeaders())
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", recorder.Code)
	}
}

// Test flow:
//  1. Make the operator answer a delete with a plain store failure.
//  2. Send a DELETE for escrow "7" through the admin route.
//  3. Assert the response is 500.
func TestAFailedDeleteIsA500(t *testing.T) {
	live := newHarness(t)
	live.operations.err = errStoreUnavailable
	recorder := live.request(t, http.MethodDelete, "/v1/admin/devshards/7", "", adminHeaders())
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", recorder.Code)
	}
}

// Test flow:
//  1. Make the control store's list call fail.
//  2. GET the admin devshards list.
//  3. Assert the response is 500.
func TestAStoreFailureOnTheOperatorRoutesIsA500(t *testing.T) {
	live := newHarness(t)
	live.control.listErr = errStoreUnavailable
	recorder := live.request(t, http.MethodGet, "/v1/admin/devshards", "", adminHeaders())
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", recorder.Code)
	}
}

// Test flow:
//  1. Mark escrow "7" busy.
//  2. POST a settle for it twice, once with no body and once with `force: true`.
//  3. Assert both attempts answer 409.
//  4. Assert no "settle" call was recorded, so a body-supplied force cannot buy past the busy check.
func TestSettlingABusyDevshardIsRefusedAndCannotBeForced(t *testing.T) {
	live := newHarness(t)
	live.escrows.busy["7"] = true
	for _, body := range []string{"", `{"force":true}`} {
		refused := live.request(t, http.MethodPost, "/v1/admin/devshards/7/settle", body, adminHeaders())
		if refused.Code != http.StatusConflict {
			t.Fatalf("settle with body %q: got %d (%s), want 409", body, refused.Code, refused.Body.String())
		}
	}
	if calls := live.operations.recordedCalls(); slices.Contains(calls, "settle") {
		t.Fatalf("a busy devshard reached the settle path: %v", calls)
	}
}

// Test flow:
//  1. Seed the control store with one devshard record.
//  2. GET the admin devshards list.
//  3. Assert the response is 200 and its body contains that escrow's ID.
func TestAnAdminListReflectsTheStore(t *testing.T) {
	live := newHarness(t)
	live.control.devshards = []store.DevshardRecord{{EscrowID: "42", Model: "kimi", Active: true}}
	recorder := live.request(t, http.MethodGet, "/v1/admin/devshards", "", adminHeaders())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status: got %d", recorder.Code)
	}
	if got := recorder.Body.String(); !strings.Contains(got, `"id":"42"`) {
		t.Fatalf("body: %s", got)
	}
}

// Test flow:
//  1. Seed the control store with a devshard whose session is resident and one whose session is not.
//  2. GET the admin devshards list.
//  3. Assert the resident one carries its runtime with the session's nonce and the other carries none: an operator and the load harness tell a live escrow from a stored row by it.
func TestAnAdminListMarksTheEscrowsWithAResidentSession(t *testing.T) {
	live := newHarness(t)
	session, machine := newLiveSession(t)
	live.escrows.sessions[liveEscrowID] = registry.NewSessionHandle(session, machine)
	live.control.devshards = []store.DevshardRecord{{EscrowID: liveEscrowID, Model: "qwen", Active: true}, {EscrowID: "42", Model: "qwen"}}

	recorder := live.request(t, http.MethodGet, "/v1/admin/devshards", "", adminHeaders())

	var listed struct {
		Devshards []struct {
			ID      string `json:"id"`
			Runtime *struct {
				Nonce uint64 `json:"nonce"`
			} `json:"runtime"`
		} `json:"devshards"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &listed); err != nil || len(listed.Devshards) != 2 {
		t.Fatalf("list = %s (%v), want two devshards", recorder.Body.String(), err)
	}
	resident, stored := listed.Devshards[0], listed.Devshards[1]
	if resident.Runtime == nil || resident.Runtime.Nonce != session.Nonce() {
		t.Fatalf("resident devshard runtime = %+v, want the session's nonce %d", resident.Runtime, session.Nonce())
	}
	if stored.Runtime != nil {
		t.Fatalf("stored-only devshard runtime = %+v, want none", stored.Runtime)
	}
}

// Test flow:
//  1. Make the control store's list call fail and install a log capture.
//  2. GET the admin devshards list.
//  3. Assert an "admin request failed" line was logged at error with the 500 status, the route, and the store failure's text.
func TestAFailedAdminOperationIsLogged(t *testing.T) {
	entries := logcapture.Install(t)
	live := newHarness(t)
	live.control.listErr = errStoreUnavailable

	live.request(t, http.MethodGet, "/v1/admin/devshards", "", adminHeaders())

	entry, found := entries.Find("admin request failed")
	if !found {
		t.Fatalf("a 500 on an admin route left no log line: %+v", entries.All())
	}
	if entry.Level != "error" {
		t.Fatalf("level = %q, want error", entry.Level)
	}
	if got := logcapture.Field(entry, "status"); got != http.StatusInternalServerError {
		t.Fatalf("status field = %v, want 500", got)
	}
	if got := logcapture.Field(entry, "route"); got != "/v1/admin/devshards" {
		t.Fatalf("route field = %v", got)
	}
	if got, _ := logcapture.Field(entry, "error").(string); !strings.Contains(got, errStoreUnavailable.Error()) {
		t.Fatalf("error field = %q, want the store failure", got)
	}
}

// Test flow:
//  1. Install a log capture and send an admin GET request with no admin key.
//  2. Assert an "admin request refused" line was logged at warn with a 401 status.
func TestAnUnkeyedAdminCallIsLogged(t *testing.T) {
	entries := logcapture.Install(t)
	live := newHarness(t)

	live.request(t, http.MethodGet, "/v1/admin/devshards", "", nil)

	entry, found := entries.Find("admin request refused")
	if !found {
		t.Fatalf("an unkeyed admin call left no log line: %+v", entries.All())
	}
	if entry.Level != "warn" {
		t.Fatalf("level = %q, want warn", entry.Level)
	}
	if got := logcapture.Field(entry, "status"); got != http.StatusUnauthorized {
		t.Fatalf("status field = %v, want 401", got)
	}
}

// Test flow:
//  1. Install a log capture and send a properly keyed admin GET request.
//  2. Assert neither an "admin request failed" nor an "admin request refused" line was logged.
func TestASuccessfulAdminOperationIsNotLoggedAsAFailure(t *testing.T) {
	entries := logcapture.Install(t)
	live := newHarness(t)

	live.request(t, http.MethodGet, "/v1/admin/devshards", "", adminHeaders())

	if _, found := entries.Find("admin request failed"); found {
		t.Fatalf("a 200 was logged as a failure: %+v", entries.All())
	}
	if _, found := entries.Find("admin request refused"); found {
		t.Fatalf("a 200 was logged as a refusal: %+v", entries.All())
	}
}

var errStoreUnavailable = errors.New("store unavailable")

// Test flow:
//  1. Make the escrow create fail because the wallet cannot pay for the amount and the fee.
//  2. POST an escrow create.
//  3. Assert it answers 402 with the wallet's shortfall, not a 502 that reads like a chain outage.
func TestAnEscrowCreateTheWalletCannotPayForAnswersPaymentRequired(t *testing.T) {
	live := newHarness(t)
	live.operations.err = fmt.Errorf("creating escrow for qwen/regular: %w", &chain.WalletUnderfundedError{Address: "gonka1wallet", Have: 5, Need: 11})

	response := live.request(t, http.MethodPost, "/v1/admin/escrows", `{"model_id":"qwen","amount":10,"private_key_env":"KEY"}`, adminHeaders())

	if response.Code != http.StatusPaymentRequired {
		t.Fatalf("status: got %d (%s), want 402", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "gonka1wallet holds 5") {
		t.Fatalf("body %s does not name the wallet's shortfall", response.Body.String())
	}
}

// Test flow:
//  1. Map a create refused below the model's floor, wrapped the way `createEscrow` returns it, through `statusForError`.
//  2. Assert it resolves to HTTP 400: the operator's amount is the bad input, not the chain.
func TestACreateBelowTheModelsFloorIsAnsweredAsABadRequest(t *testing.T) {
	refused := fmt.Errorf("creating escrow for model-a/regular: %w: amount 1000, floor 191000", escrow.ErrAmountBelowFloor)

	if got := statusForError(refused); got != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", got, http.StatusBadRequest)
	}
}
