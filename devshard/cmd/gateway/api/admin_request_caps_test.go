package api

import (
	"net/http"
	"testing"
)

// Test flow:
//  1. POST a settings body naming only `max_burns_per_request`.
//  2. Read the overrides the operations applied.
//  3. Assert the response is 200 and the applied overrides carry the per-request burn cap, so the next request reads it from the live settings.
func TestAPostedSettingsPatchReachesTheBurnCapOfARequest(t *testing.T) {
	live := newHarness(t)

	recorder := live.request(t, http.MethodPost, "/v1/admin/settings", `{"max_burns_per_request":4}`, adminHeaders())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d (%s), want 200", recorder.Code, recorder.Body.String())
	}
	applied := live.operations.appliedOverrides()
	if applied.MaxBurnsPerRequest == nil || *applied.MaxBurnsPerRequest != 4 {
		t.Fatalf("applied burn cap %v, want 4", applied.MaxBurnsPerRequest)
	}
}

// Test flow:
//  1. POST a settings body naming only `engine_max_started_attempts_per_request`.
//  2. Read the overrides the operations applied.
//  3. Assert the response is 200 and the applied overrides carry the per-request attempt cap, so the next request reads it from the live settings.
func TestAPostedSettingsPatchReachesTheAttemptCapOfARequest(t *testing.T) {
	live := newHarness(t)

	recorder := live.request(t, http.MethodPost, "/v1/admin/settings", `{"engine_max_started_attempts_per_request":2}`, adminHeaders())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d (%s), want 200", recorder.Code, recorder.Body.String())
	}
	applied := live.operations.appliedOverrides()
	if applied.EngineMaxStartedAttemptsPerRequest == nil || *applied.EngineMaxStartedAttemptsPerRequest != 2 {
		t.Fatalf("applied attempt cap %v, want 2", applied.EngineMaxStartedAttemptsPerRequest)
	}
}
