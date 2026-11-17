package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
)

// Test flow:
//  1. Define a table of chain phase snapshots and PoC modes, varying across: an admitting inference phase, a PoC generation block, a PoC validation block, a confirmation-PoC-generation block, relaxed mode admitting a blocked PoC generation, relaxed mode admitting a blocked confirmation PoC, and an unnamed block reason.
//  2. For each case, run `admission` against the snapshot, mode, and a stale epoch.
//  3. Assert whether it blocked matches the case's expectation.
//  4. For a blocked case, assert the error message and that `statusForError` maps it to 503.
func TestAdmissionFoldsRelaxedModeOverTheRawChainState(t *testing.T) {
	testCases := []struct {
		name        string
		snapshot    chain.PhaseSnapshot
		mode        string
		wantBlocked bool
		wantMessage string
	}{
		{
			name:     "inference phase admits",
			snapshot: chain.PhaseSnapshot{EpochPhase: chain.EpochPhaseInference},
			mode:     config.PoCModeOff,
		},
		{
			name:        "poc generation blocks",
			snapshot:    chain.PhaseSnapshot{RequestsBlocked: true, BlockReason: chain.BlockReasonPoC, EpochPhase: chain.EpochPhasePoCGenerate},
			mode:        config.PoCModeOff,
			wantBlocked: true,
			wantMessage: "devshard temporarily unavailable during PoC generation",
		},
		{
			name:        "poc validation blocks",
			snapshot:    chain.PhaseSnapshot{RequestsBlocked: true, BlockReason: chain.BlockReasonPoC, EpochPhase: chain.EpochPhasePoCValidate},
			mode:        config.PoCModeOff,
			wantBlocked: true,
			wantMessage: "devshard temporarily unavailable during PoC validation",
		},
		{
			name:        "confirmation poc blocks",
			snapshot:    chain.PhaseSnapshot{RequestsBlocked: true, BlockReason: chain.BlockReasonConfirmationPoC, ConfirmationPoCPhase: chain.ConfirmationPoCGeneration},
			mode:        config.PoCModeOff,
			wantBlocked: true,
			wantMessage: "devshard temporarily unavailable during confirmation PoC generation",
		},
		{
			name:     "relaxed mode admits a blocked poc generation",
			snapshot: chain.PhaseSnapshot{RequestsBlocked: true, BlockReason: chain.BlockReasonPoC, EpochPhase: chain.EpochPhasePoCGenerate},
			mode:     config.PoCModeRelaxed,
		},
		{
			name:     "relaxed mode admits a blocked confirmation poc",
			snapshot: chain.PhaseSnapshot{RequestsBlocked: true, BlockReason: chain.BlockReasonConfirmationPoC, ConfirmationPoCPhase: chain.ConfirmationPoCValidation},
			mode:     config.PoCModeRelaxed,
		},
		{
			name:        "an unnamed reason still blocks",
			snapshot:    chain.PhaseSnapshot{RequestsBlocked: true},
			mode:        config.PoCModeOff,
			wantBlocked: true,
			wantMessage: "devshard temporarily unavailable during chain admission controls",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := admission(testCase.snapshot, config.Modes{PoCMode: testCase.mode}, staleTestEpoch, 0)
			if testCase.wantBlocked != (err != nil) {
				t.Fatalf("blocked: got %v, want %v", err != nil, testCase.wantBlocked)
			}
			if err == nil {
				return
			}
			if err.Error() != testCase.wantMessage {
				t.Fatalf("message: got %q, want %q", err.Error(), testCase.wantMessage)
			}
			if got := statusForError(err); got != http.StatusServiceUnavailable {
				t.Fatalf("status: got %d, want 503", got)
			}
		})
	}
}

// Test flow:
//  1. Build a chain snapshot blocked for PoC generation.
//  2. Start a harness in PoC-off mode with that snapshot and send a chat completion.
//  3. Assert the request is rejected with 503, and that it never took a limiter slot or started a race.
//  4. Start a second harness with the same snapshot in relaxed mode and send the identical chat completion.
//  5. Assert it is served with 200 and started exactly one race.
func TestChatIsRejectedDuringPoCAndServedUnderRelaxedMode(t *testing.T) {
	blocked := chain.PhaseSnapshot{RequestsBlocked: true, BlockReason: chain.BlockReasonPoC, EpochPhase: chain.EpochPhasePoCGenerate}

	rejecting := newHarness(t)
	rejecting.snapshots.snapshot = blocked
	rejecting.swapConfig(func(next *config.Config) { next.Modes.PoCMode = config.PoCModeOff })
	recorder := rejecting.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("blocked chat: got %d, want 503", recorder.Code)
	}
	if got := rejecting.limiter.acquires.Load(); got != 0 {
		t.Fatalf("a blocked request took %d limiter slots; admission must run before the limiter", got)
	}
	if got := rejecting.inference.runs.Load(); got != 0 {
		t.Fatalf("a blocked request started %d races", got)
	}

	relaxed := newHarness(t)
	relaxed.snapshots.snapshot = blocked
	relaxed.swapConfig(func(next *config.Config) { next.Modes.PoCMode = config.PoCModeRelaxed })
	served := relaxed.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)
	if served.Code != http.StatusOK {
		t.Fatalf("relaxed mode must serve the same snapshot: got %d (%s)", served.Code, served.Body.String())
	}
	if got := relaxed.inference.runs.Load(); got != 1 {
		t.Fatalf("relaxed mode races: got %d, want 1", got)
	}
}

// Test flow:
//  1. Start a harness whose chain snapshot is in PoC generation.
//  2. GET /v1/status.
//  3. Assert the status names that epoch phase: an operator, and the load harness's phase check, read the chain's phase from the gateway the way they read it from devshardctl.
func TestStatusReportsTheChainsEpochPhase(t *testing.T) {
	live := newHarness(t)
	live.snapshots.snapshot = chain.PhaseSnapshot{RequestsBlocked: true, BlockReason: chain.BlockReasonPoC, EpochPhase: chain.EpochPhasePoCGenerate}

	recorder := live.request(t, http.MethodGet, "/v1/status", "", nil)

	var status struct {
		ChainPhase string `json:"chain_phase"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil || status.ChainPhase != string(chain.EpochPhasePoCGenerate) {
		t.Fatalf("status = %s (%v), want chain_phase %q", recorder.Body.String(), err, chain.EpochPhasePoCGenerate)
	}
}

// Test flow:
//  1. Admit a chat request while the chain allows requests, and have the chain block them for proof-of-compute while the request waits in the limiter, with relaxed mode off.
//  2. Assert the request is refused 503 once it leaves the limiter, its slot is given back, and no race starts: a nonce committed during the block would be voted against.
func TestARequestThatWaitedOutABlockInTheLimiterIsRefused(t *testing.T) {
	live := newHarness(t)
	live.swapConfig(func(next *config.Config) { next.Modes.PoCMode = config.PoCModeOff })
	live.limiter.onAcquire = func() {
		live.snapshots.snapshot = chain.PhaseSnapshot{RequestsBlocked: true, BlockReason: chain.BlockReasonPoC, EpochPhase: chain.EpochPhasePoCGenerate}
	}

	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d (%s), want 503", recorder.Code, recorder.Body.String())
	}
	if acquired, released := live.limiter.acquires.Load(), live.limiter.releases.Load(); acquired != released {
		t.Fatalf("limiter slots acquired %d, released %d, want every slot given back", acquired, released)
	}
	if got := live.inference.runs.Load(); got != 0 {
		t.Fatalf("races started: got %d, want 0", got)
	}
}
