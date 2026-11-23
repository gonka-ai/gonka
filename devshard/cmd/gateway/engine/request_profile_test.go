package engine

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/scheduler"
)

// Test flow:
//  1. Build a `raceCoordinator` for a request with a known id, model, escrow, input tokens, their source and bytes.
//  2. Build its request profile with a params string.
//  3. Assert the profile carries the request id, model, escrow, input tokens, their source, bytes and params through unchanged.
func TestAPickCarriesTheRequestItServes(t *testing.T) {
	coordinator := &raceCoordinator{
		request:  raceRequest{Request: Request{RequestID: "request-3", Model: testModel, InputTokens: 12, InputTokensSource: filters.InputTokensTokenizerExact, InputBytes: 47}},
		escrowID: "escrow-1",
		burns:    &scheduler.BurnTally{},
	}

	profile := coordinator.requestProfile("params")

	require.Equal(t, scheduler.RequestProfile{
		RequestID: "request-3", Model: testModel, Escrow: "escrow-1", InputTokens: 12, InputTokensSource: filters.InputTokensTokenizerExact, InputBytes: 47, Params: "params",
		Burns: coordinator.burns,
	}, profile)
}

// Test flow:
//  1. Build a coordinator and take the profile of its first pick and the profile of an escalation.
//  2. Compare the burn tallies the two profiles carry.
//  3. Assert both share the coordinator's one tally, so burns during any attempt count against the same request.
func TestEveryAttemptOfARaceSharesOneBurnTally(t *testing.T) {
	coordinator := &raceCoordinator{burns: &scheduler.BurnTally{}}

	first := coordinator.requestProfile("params")
	escalation := coordinator.requestProfile("params")

	require.NotNil(t, first.Burns)
	require.Same(t, first.Burns, escalation.Burns)
}
