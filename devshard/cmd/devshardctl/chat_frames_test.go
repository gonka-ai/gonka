package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
)

func TestConfigureChatFramesFromEnv(t *testing.T) {
	t.Cleanup(func() { chatFrameSchedule = nil })

	t.Setenv("GATEWAY_CHAT_FRAME_EVENTS", "")
	t.Setenv("GATEWAY_CHAT_FRAME_SEPARATE", "")
	configureChatFramesFromEnv()
	require.Nil(t, chatFrameSchedule, "unset leaves each executor its default")

	t.Setenv("GATEWAY_CHAT_FRAME_EVENTS", "10")
	configureChatFramesFromEnv()
	require.Equal(t, &host.ChatFrameSchedule{Every: 10}, chatFrameSchedule)

	t.Setenv("GATEWAY_CHAT_FRAME_SEPARATE", "true")
	configureChatFramesFromEnv()
	require.Equal(t, &host.ChatFrameSchedule{Every: 10, Separate: true}, chatFrameSchedule)

	t.Setenv("GATEWAY_CHAT_FRAME_EVENTS", "")
	configureChatFramesFromEnv()
	require.Equal(t, &host.ChatFrameSchedule{Separate: true}, chatFrameSchedule, "separate alone keeps the executor's group size")

	t.Setenv("GATEWAY_CHAT_FRAME_EVENTS", "nope")
	t.Setenv("GATEWAY_CHAT_FRAME_SEPARATE", "")
	configureChatFramesFromEnv()
	require.Nil(t, chatFrameSchedule, "an unreadable value falls back to the default")
}
