package registry

import (
	"testing"

	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/scheduler"
	"devshard/host"
	"devshard/state"
	"devshard/user"
)

// Test flow:
//  1. For each of four request bodies (the table varies whether max_tokens, max_completion_tokens, or neither is set), normalize the request through `filters.NormalizeRequest`.
//  2. Assert normalization succeeds and the resulting MaxTokens is at or above `state.MinReservationTokens`, the chain's own floor.
//  3. Advance a live stream, committing a nonce intent built from the normalized body.
//  4. Assert the commit succeeds and a nonce was prepared.
func TestWhatTheFiltersProduceTheChainAccepts(t *testing.T) {
	for _, body := range []string{
		`{"messages":[{"role":"user","content":"x"}],"max_tokens":1}`,
		`{"messages":[{"role":"user","content":"x"}],"max_tokens":8}`,
		`{"messages":[{"role":"user","content":"x"}],"max_completion_tokens":8}`,
		`{"messages":[{"role":"user","content":"x"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			normalized, err := filters.NormalizeRequest([]byte(body), filters.Options{
				RoutedModel:      "qwen",
				DefaultMaxTokens: 3072,
				MaxTokensCap:     4096,
			})
			if err != nil {
				t.Fatalf("NormalizeRequest() = %v, want nil", err)
			}
			if normalized.MaxTokens < state.MinReservationTokens {
				t.Fatalf("MaxTokens = %d, under the chain's floor %d before the chain ever sees it", normalized.MaxTokens, state.MinReservationTokens)
			}

			stream, _, _ := newLiveStream(t, 1, 1, 1)
			prepared, err := stream.Advance(func(scheduler.HostBinding) scheduler.NonceIntent {
				return scheduler.NonceIntent{Commit: true, Params: user.InferenceParams{
					Model:       "qwen",
					Prompt:      normalized.Body,
					InputLength: uint64(len(normalized.Body)),
					MaxTokens:   normalized.MaxTokens,
				}}
			})
			if err != nil {
				t.Fatalf("committing what the filters produced = %v, want nil", err)
			}
			if prepared == nil {
				t.Fatal("no nonce was committed")
			}
		})
	}
}

// Test flow:
//  1. Build a live stream over a real state machine and advance it with a ghost intent.
//  2. Assert the commit succeeds and the committed record reserves `state.MinReservationTokens` and costs that reservation at the escrow's token price.
//  3. Assert the payload a refusal-timeout verifier rebuilds for that nonce passes `host.VerifyPayload` against the committed record.
func TestAGhostBurnReservesTheStateMachineFloor(t *testing.T) {
	stream, session, _ := newLiveStream(t, 1, 1, 1)

	prepared, err := stream.Advance(func(scheduler.HostBinding) scheduler.NonceIntent {
		return scheduler.NonceIntent{Commit: true, Ghost: true}
	})
	if err != nil {
		t.Fatalf("burning a nonce = %v, want nil", err)
	}
	if prepared == nil {
		t.Fatal("no nonce was burned")
	}
	machine := session.(sessionHandle).machine
	record, found := machine.SnapshotState().Inferences[prepared.Nonce()]
	if !found {
		t.Fatalf("no committed record for burned nonce %d", prepared.Nonce())
	}
	if record.MaxTokens != state.MinReservationTokens {
		t.Errorf("ghost MaxTokens = %d, want the state machine floor %d", record.MaxTokens, state.MinReservationTokens)
	}
	wantCost, err := state.ReservedCost(uint64(len(ghostPrompt)), state.MinReservationTokens, machine.Config().TokenPrice)
	if err != nil {
		t.Fatalf("ReservedCost = %v, want nil", err)
	}
	if record.ReservedCost != wantCost {
		t.Errorf("ghost ReservedCost = %d, want %d", record.ReservedCost, wantCost)
	}
	payload := prepared.(*user.PreparedInference).Payload()
	if err := host.VerifyPayload(payload, record.PromptHash, record.Model, record.InputLength, record.MaxTokens, record.StartedAt); err != nil {
		t.Errorf("VerifyPayload(ghost payload) = %v, want nil", err)
	}
}
