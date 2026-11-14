package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
)

func TestConfigureChatFramesFromEnv(t *testing.T) {
	t.Cleanup(func() { chatFrameSchedule = nil })

	set := func(join, signEvery, events, separate string) {
		t.Helper()
		t.Setenv("GATEWAY_CHAT_FRAME_JOIN", join)
		t.Setenv("GATEWAY_CHAT_FRAME_SIGN_EVERY", signEvery)
		t.Setenv("GATEWAY_CHAT_FRAME_EVENTS", events)
		t.Setenv("GATEWAY_CHAT_FRAME_SEPARATE", separate)
	}

	set("", "", "10", "true")
	configureChatFramesFromEnv()
	require.Nil(t, chatFrameSchedule, "unset leaves each executor its default, and the old variables are ignored")

	set("5", "13", "99", "true")
	configureChatFramesFromEnv()
	require.Equal(t, &host.ChatFrameSchedule{Join: 5, SignEvery: 13}, chatFrameSchedule)

	set("100", "13", "", "")
	configureChatFramesFromEnv()
	require.Equal(t, &host.ChatFrameSchedule{Join: 64, SignEvery: 13}, chatFrameSchedule)

	set("5", "5000", "", "")
	configureChatFramesFromEnv()
	require.Equal(t, &host.ChatFrameSchedule{Join: 5, SignEvery: 4096}, chatFrameSchedule)

	set("nope", "13", "", "")
	configureChatFramesFromEnv()
	require.Nil(t, chatFrameSchedule, "a join that is not an integer leaves the default")

	set("5", "nope", "", "")
	configureChatFramesFromEnv()
	require.Nil(t, chatFrameSchedule, "a sign interval that is not an integer leaves the default")
}
