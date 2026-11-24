package env

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Read an unset schedule, then a valid explicit schedule.
//  2. Set malformed event and boolean values together.
//  3. Assert unset values stay nil, valid values survive and both parse failures are reported.
func TestChatFrameEnvironmentIsStrict(t *testing.T) {
	t.Setenv("GATEWAY_CHAT_FRAME_EVENTS", "")
	t.Setenv("GATEWAY_CHAT_FRAME_SEPARATE", "")
	values, err := Load()
	require.NoError(t, err)
	require.Nil(t, values.ChatFrameEvents)
	require.Nil(t, values.ChatFrameSeparate)
	t.Setenv("GATEWAY_CHAT_FRAME_EVENTS", "10")
	t.Setenv("GATEWAY_CHAT_FRAME_SEPARATE", "true")
	values, err = Load()
	require.NoError(t, err)
	require.Equal(t, int64(10), *values.ChatFrameEvents)
	require.True(t, *values.ChatFrameSeparate)
	t.Setenv("GATEWAY_CHAT_FRAME_EVENTS", "five")
	t.Setenv("GATEWAY_CHAT_FRAME_SEPARATE", "sometimes")
	_, err = Load()
	require.ErrorContains(t, err, "GATEWAY_CHAT_FRAME_EVENTS")
	require.ErrorContains(t, err, "GATEWAY_CHAT_FRAME_SEPARATE")
}
