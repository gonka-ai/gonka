package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/env"
)

// Test flow:
//  1. Build the default schedule, then explicit schedules at both legal boundaries.
//  2. Try zero, negative, above-limit and overflowing event counts.
//  3. Assert valid schedules are retained and invalid values fail configuration validation.
func TestChatFrameScheduleIsDefaultedAndBounded(t *testing.T) {
	t.Parallel()
	configuration, err := Build(env.Values{}, Overrides{})
	require.NoError(t, err)
	require.Equal(t, ChatFrames{Events: 5}, configuration.ChatFrames)
	for _, count := range []int64{1, 64} {
		separate := true
		configuration, err = Build(env.Values{ChatFrameEvents: &count, ChatFrameSeparate: &separate}, Overrides{})
		require.NoError(t, err)
		require.Equal(t, ChatFrames{Events: count, Separate: true}, configuration.ChatFrames)
	}
	for _, count := range []int64{-1, 0, 65, 1 << 32} {
		_, err = Build(env.Values{ChatFrameEvents: &count}, Overrides{})
		require.ErrorIs(t, err, ErrInvalid)
		require.ErrorContains(t, err, "chat_frame_events")
	}
}
