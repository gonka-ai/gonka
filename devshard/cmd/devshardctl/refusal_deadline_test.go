package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
)

// A verifier measures the refusal deadline from the record's StartedAt against its own clock. When
// that difference cannot reach the timeout, every vote is a guaranteed reject, so the gateway must
// not spend a round on it — and must say so, because the vote result carries no reason of its own.
func TestRefusalDeadline_UnreachableDeadlineSkipsTheRoundInsteadOfLosingIt(t *testing.T) {
	shortRefusalWindow(t)
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()
	params := defaultParams()
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)

	// A stamp in the wrong unit lands far in the future, which is exactly how a millisecond
	// StartedAt reaches a verifier reading seconds.
	result, err := env.session.HandleTimeout(context.Background(), prepared.Nonce(), time.Now(),
		&host.InferencePayload{
			Prompt:      params.Prompt,
			Model:       params.Model,
			InputLength: params.InputLength,
			MaxTokens:   params.MaxTokens,
			StartedAt:   time.Now().UnixMilli(),
		})

	require.Error(t, err)
	require.Equal(t, "skipped", result.Outcome)
	require.Equal(t, "refusal_deadline_unreachable", result.DetailReason)
	require.Nil(t, env.killables[prepared.HostIdx()].LastRequest(),
		"a round that cannot pass must cost no verifier traffic")
}

// The guard must not stand in the way of a deadline that has genuinely run.
func TestRefusalDeadline_APassedDeadlineStillCollectsVotes(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()
	params := defaultParams()
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)

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
