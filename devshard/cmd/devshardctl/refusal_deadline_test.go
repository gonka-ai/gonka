package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/types"
)

func TestRefusalDeadline_PayloadStartedAtUnitDoesNotMoveTheDeadline(t *testing.T) {
	shortRefusalWindow(t)
	env := setupLiveFloorProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()
	params := defaultParams()
	prepared, err := env.session.PrepareInference(context.Background(), params)
	require.NoError(t, err)

	result, err := env.session.HandleTimeout(context.Background(), prepared.Nonce(), time.Now(),
		&host.InferencePayload{
			Prompt:      params.Prompt,
			Model:       params.Model,
			InputLength: params.InputLength,
			MaxTokens:   params.MaxTokens,
			StartedAt:   time.Now().UnixMilli(),
		})

	require.Error(t, err)
	require.Equal(t, "applied", result.Outcome)
	require.True(t, result.Applied)
}

func TestRefusalDeadline_APassedDeadlineStillCollectsVotes(t *testing.T) {
	env := setupLiveFloorProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()
	params := defaultParams()
	prepared, err := env.session.PrepareInference(context.Background(), params)
	require.NoError(t, err)

	// An hour on the wall clock makes the vote runnable. The timeout lands
	// once the floor's block time has passed the start by the refusal window.
	require.Eventually(t, func() bool {
		return env.sm.TimeoutDue(prepared.Nonce(), types.TimeoutReason_TIMEOUT_REASON_REFUSED) == nil
	}, 5*time.Second, 20*time.Millisecond)
	startedAt := time.Now().Add(-time.Hour)
	result, err := env.session.HandleTimeout(context.Background(), prepared.Nonce(), startedAt,
		&host.InferencePayload{
			Prompt:      params.Prompt,
			Model:       params.Model,
			InputLength: params.InputLength,
			MaxTokens:   params.MaxTokens,
			StartedAt:   startedAt.Unix(),
		})

	require.Error(t, err, "the inference did time out")
	require.NotEqual(t, "skipped", result.Outcome)
	require.True(t, result.Applied, "an hour past the deadline is votable")
}
