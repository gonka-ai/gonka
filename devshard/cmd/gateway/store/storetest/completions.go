package storetest

import (
	"errors"
	"testing"

	"devshard/host"
	"devshard/user"
)

// Test flow:
//  1. Save a completion, save it again unchanged, then save a different payload at its nonce.
//  2. Assert the repeat is accepted and the different payload is a conflict.
//  3. Probe for a saved and an absent nonce and another escrow with `HasInferenceCompletion`.
//  4. Save an earlier nonce and list, asserting nonce order; assert another escrow lists nothing.
//  5. Delete one entry and assert a second delete is a no-op.
func completionJournal(t *testing.T, open Opener) {
	gatewayStore := open()
	entry := func(nonce uint64, prompt string) user.InferenceCompletion {
		return user.InferenceCompletion{Nonce: nonce, PreparedAt: 1_700_000_000, Payload: host.InferencePayload{Prompt: []byte(prompt), Model: "qwen", InputLength: 3, MaxTokens: 9, StartedAt: 5}}
	}
	if err := gatewayStore.SaveInferenceCompletion("301", entry(5, "p")); err != nil {
		t.Fatalf("SaveInferenceCompletion(): %v", err)
	}
	if err := gatewayStore.SaveInferenceCompletion("301", entry(5, "p")); err != nil {
		t.Fatalf("SaveInferenceCompletion(same): %v", err)
	}
	if err := gatewayStore.SaveInferenceCompletion("301", entry(5, "other")); !errors.Is(err, user.ErrInferenceCompletionConflict) {
		t.Fatalf("SaveInferenceCompletion(different) = %v, want ErrInferenceCompletionConflict", err)
	}
	if err := gatewayStore.SaveInferenceCompletion("301", entry(2, "q")); err != nil {
		t.Fatalf("SaveInferenceCompletion(2): %v", err)
	}
	for _, probe := range []struct {
		escrowID string
		nonce    uint64
		want     bool
	}{{"301", 5, true}, {"301", 6, false}, {"302", 5, false}} {
		if got, err := gatewayStore.HasInferenceCompletion(probe.escrowID, probe.nonce); err != nil || got != probe.want {
			t.Fatalf("HasInferenceCompletion(%s, %d) = %v, %v; want %v", probe.escrowID, probe.nonce, got, err, probe.want)
		}
	}
	listed, err := gatewayStore.ListInferenceCompletions("301")
	if err != nil || len(listed) != 2 || listed[0].Nonce != 2 || listed[1].Nonce != 5 || string(listed[1].Payload.Prompt) != "p" {
		t.Fatalf("ListInferenceCompletions() = %+v, %v; want nonces 2 and 5", listed, err)
	}
	if other, err := gatewayStore.ListInferenceCompletions("302"); err != nil || len(other) != 0 {
		t.Fatalf("ListInferenceCompletions(302) = %+v, %v; want none", other, err)
	}
	for range 2 {
		if err := gatewayStore.DeleteInferenceCompletion("301", 2); err != nil {
			t.Fatalf("DeleteInferenceCompletion(): %v", err)
		}
	}
	listed, _ = gatewayStore.ListInferenceCompletions("301")
	if len(listed) != 1 || listed[0].Nonce != 5 {
		t.Fatalf("after delete = %+v, want only nonce 5", listed)
	}
}
