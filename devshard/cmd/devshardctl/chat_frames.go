package main

import (
	"context"

	"devshard/host"
)

// chatFrameSchedule is what every inference asks the executor for, read once
// at startup. Nil leaves each executor its own default: the first output
// alone, then five events joined into one signed frame.
var chatFrameSchedule *host.ChatFrameSchedule

// configureChatFramesFromEnv lets an operator try another frame schedule
// without a host release. GATEWAY_CHAT_FRAME_EVENTS is how many events share
// one signature; GATEWAY_CHAT_FRAME_SEPARATE keeps one flush per event, so
// tokens arrive one by one and only every N-th frame carries a signature.
func configureChatFramesFromEnv() {
	every := readInt64Env("GATEWAY_CHAT_FRAME_EVENTS", 0)
	separate := readBoolEnv("GATEWAY_CHAT_FRAME_SEPARATE", false)
	if every <= 0 && !separate {
		chatFrameSchedule = nil
		return
	}
	schedule := &host.ChatFrameSchedule{Separate: separate}
	if every > 0 {
		schedule.Every = uint32(min(every, 1<<16))
	}
	chatFrameSchedule = schedule
}

// logChatFrameCheck records what the gateway saw of an attempt's frame
// signatures. A signature that does not hold is logged and nothing else:
// acting on it takes another participant challenging the stream, which is a
// separate protocol change. An attempt with no signed frame at all is an
// executor that predates them, and is not logged.
func logChatFrameCheck(ctx context.Context, inf *inflight) {
	if inf == nil || inf.resp == nil || inf.probe {
		return
	}
	check := inf.resp.ChatFrames
	if check.Invalid == "" {
		return
	}
	logInferenceStage(ctx, inf.escrowID, inf.nonce, "chat_frame_signature_invalid",
		"host", inf.hostID,
		"frames", check.Frames,
		"signed", check.Signed,
		"error", check.Invalid,
	)
}
