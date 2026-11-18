package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"

	"devshard/host"
	"devshard/transport"
)

// chatFrameSchedule is what every inference asks the executor for, read once
// at startup. Nil leaves each executor its own default: join 5, sign every
// 13th chunk.
var chatFrameSchedule *host.ChatFrameSchedule

// configureChatFramesFromEnv lets an operator try another frame schedule
// without a host release. GATEWAY_CHAT_FRAME_JOIN is how many content tokens
// share a chunk after the first. The silence between frames is about that
// many token intervals, and the inter-chunk stall allowance grows with it
// from the budget the default join of 5 fits in. GATEWAY_CHAT_FRAME_SIGN_EVERY
// is how many chunks share one signature. Either value that is not an integer
// leaves the executor on its default.
func configureChatFramesFromEnv() {
	join, joinOK := readScheduleUint("GATEWAY_CHAT_FRAME_JOIN", transport.MaxChatFrameJoin)
	signEvery, signOK := readScheduleUint("GATEWAY_CHAT_FRAME_SIGN_EVERY", transport.MaxChatFrameSignEvery)
	if !joinOK || !signOK || (join == 0 && signEvery == 0) {
		chatFrameSchedule = nil
		return
	}
	chatFrameSchedule = &host.ChatFrameSchedule{Join: join, SignEvery: signEvery}
}

// readScheduleUint parses name as a non-negative integer. An empty value is
// zero and ok. A value that is not an integer is not ok. Values above max
// are capped.
func readScheduleUint(name string, max uint32) (uint32, bool) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, true
	}
	v, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		log.Printf("invalid %s=%q, leaving the chat frame schedule unset", name, raw)
		return 0, false
	}
	if v > uint64(max) {
		v = uint64(max)
	}
	return uint32(v), true
}

// logBadArtifact warns about a stream the gateway can already show is bad:
// an empty stream, a signed token count that does not match the bytes, a
// signature that does not verify, or [DONE] with no finish_reason. Later we
// can trigger voting on these artifacts.
func logBadArtifact(ctx context.Context, escrowID string, nonce uint64, stage string, kv ...any) {
	logInferenceWarn(ctx, escrowID, nonce, stage, kv...)
}

// logChatFrameCheck records a bad frame signature, a signed token count that
// does not match the content tokens in its prefix, and a [DONE] that arrived
// with no finish_reason. An attempt with no signed frame is an executor that
// predates them, and the signature checks are not logged. The missing finish
// reason is about the SSE body, so it is logged either way.
func logChatFrameCheck(ctx context.Context, inf *inflight) {
	if inf == nil || inf.resp == nil || inf.probe {
		return
	}
	check := inf.resp.ChatFrames
	if check.Invalid != "" {
		logBadArtifact(ctx, inf.escrowID, inf.nonce, "chat_frame_signature_invalid",
			"host", inf.hostID,
			"frames", check.Frames,
			"signed", check.Signed,
			"token_counts", check.TokenCounts,
			"error", check.Invalid,
		)
	}
	if check.TokenCountMismatch {
		logBadArtifact(ctx, inf.escrowID, inf.nonce, "chat_frame_token_count",
			"host", inf.hostID,
			"claimed", check.TokenCount,
			"content_tokens", check.ContentTokens,
		)
	}
	if check.DoneWithoutFinish {
		logBadArtifact(ctx, inf.escrowID, inf.nonce, "done_without_finish",
			"host", inf.hostID,
			"frames", check.Frames,
			"signed", check.Signed,
		)
	}
}
