package api

import (
	"context"
	"net/http"
	"testing"
)

// Test flow:
//  1. Make inference keep the race's `OnFinished` hook instead of running it, as a race still draining a host after its client left does.
//  2. POST a chat completion from a client that has already left and let the handler return.
//  3. Assert the limiter still counts the request and its input tokens while the race goes on.
//  4. Run the hook and assert the slot and the token budget were given back exactly once.
func TestTheLimiterSlotIsHeldUntilTheRaceFinishes(t *testing.T) {
	live := newHarness(t)
	live.inference.finishes = make(chan func(), 1)
	clientCtx, disconnect := context.WithCancel(context.Background())
	disconnect()

	live.requestWithContext(t, clientCtx, http.MethodPost, "/v1/chat/completions", chatBody, nil)

	if got := live.limiter.releases.Load(); got != 0 {
		t.Fatalf("limiter releases while the race still runs: got %d, want 0", got)
	}
	if got, want := live.limiter.tokens.Load(), live.limiter.acquired.Load(); got != want {
		t.Fatalf("input tokens held while the race still runs: got %d, want %d", got, want)
	}
	finish := <-live.inference.finishes
	finish()
	if got := live.limiter.releases.Load(); got != 1 {
		t.Fatalf("limiter releases after the race finished: got %d, want 1", got)
	}
	if got := live.limiter.tokens.Load(); got != 0 {
		t.Fatalf("input-token budget left held: got %d, want 0", got)
	}
}

// Test flow:
//  1. Make inference keep the race's `OnFinished` hook and run it before the handler is done with the reply.
//  2. POST a chat completion and let the race finish first.
//  3. Assert the slot is released exactly once, when the handler returns.
func TestTheLimiterSlotIsHeldUntilTheHandlerReturns(t *testing.T) {
	live := newHarness(t)
	live.inference.finishes = make(chan func(), 1)
	finished := make(chan struct{})
	go func() {
		(<-live.inference.finishes)()
		close(finished)
	}()

	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)
	<-finished

	if recorder.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", recorder.Code)
	}
	if got := live.limiter.releases.Load(); got != 1 {
		t.Fatalf("limiter releases: got %d, want 1", got)
	}
}

// Test flow:
//  1. Make inference run the race's `OnFinished` hook and then panic.
//  2. POST a chat completion and recover the panic the handler lets through.
//  3. Assert the slot and the token budget were given back exactly once.
func TestAPanickingRaceReturnsItsSlotOnce(t *testing.T) {
	live := newHarness(t)
	live.inference.panics = true

	recovered := func() (panicked any) {
		defer func() { panicked = recover() }()
		live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)
		return nil
	}()

	if recovered == nil {
		t.Fatal("the race's panic did not reach the caller")
	}
	if got := live.limiter.releases.Load(); got != 1 {
		t.Fatalf("limiter releases: got %d, want 1", got)
	}
	if got := live.limiter.tokens.Load(); got != 0 {
		t.Fatalf("input-token budget left held: got %d, want 0", got)
	}
}
