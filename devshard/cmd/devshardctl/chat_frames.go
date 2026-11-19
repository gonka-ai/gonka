package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"

	"devshard/host"
	"devshard/transport"
	"devshard/types"
)

// chatFramesEnabledHeader is the per-inference enable override. When unset,
// the process default from GATEWAY_CHAT_FRAMES applies.
const chatFramesEnabledHeader = "X-Devshard-Chat-Frames"

// chatFramesEnabledDefault is the process fallback for whether an inference
// asks the executor to join/sign Chat frames when the request does not set
// X-Devshard-Chat-Frames. Join and SignEvery come from session config
// (governance-frozen at escrow create), not from env.
//
// GATEWAY_CHAT_FRAMES:
//   - 1 / true / on / yes  → default enabled
//   - 0 / false / off / no → default disabled (nil ChatFrames)
//   - unset                → default enabled
var chatFramesEnabledDefault = true

// configureChatFramesFromEnv reads only the per-process enable fallback.
// Schedule knobs are session config (ChatFrameJoin / ChatFrameSignEvery).
func configureChatFramesFromEnv() {
	enabled, ok := parseChatFramesEnabled(os.Getenv("GATEWAY_CHAT_FRAMES"), true)
	if !ok {
		chatFramesEnabledDefault = false
		return
	}
	chatFramesEnabledDefault = enabled
}

// parseChatFramesEnabled parses a boolean enable switch. emptyDefault is used
// when raw is empty. ok is false when the value is not recognized.
func parseChatFramesEnabled(raw string, emptyDefault bool) (enabled, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return emptyDefault, true
	}
	switch strings.ToLower(raw) {
	case "1", "true", "on", "yes":
		return true, true
	case "0", "false", "off", "no":
		return false, true
	default:
		return false, false
	}
}

// chatFramesEnabledForRequest decides enable for one inference. The request
// header wins when present; otherwise the process default is used. An
// unrecognized header value disables signing for that inference only.
func chatFramesEnabledForRequest(r *http.Request) bool {
	if r == nil {
		return chatFramesEnabledDefault
	}
	raw := r.Header.Get(chatFramesEnabledHeader)
	if strings.TrimSpace(raw) == "" {
		return chatFramesEnabledDefault
	}
	enabled, ok := parseChatFramesEnabled(raw, chatFramesEnabledDefault)
	if !ok {
		log.Printf("invalid %s=%q, disabling chat frame signing for this request", chatFramesEnabledHeader, raw)
		return false
	}
	return enabled
}

// chatFrameScheduleForInference returns the frame schedule for one inference.
// Nil means signing is off for that inference. When enabled, Join and
// SignEvery come from the frozen session config.
func chatFrameScheduleForInference(enabled bool, cfg types.SessionConfig) *host.ChatFrameSchedule {
	if !enabled {
		return nil
	}
	join := cfg.ChatFrameJoin
	if join == 0 {
		join = types.DefaultChatFrameJoin
	}
	if join > transport.MaxChatFrameJoin {
		join = transport.MaxChatFrameJoin
	}
	signEvery := cfg.ChatFrameSignEvery
	if signEvery == 0 {
		signEvery = types.DefaultChatFrameSignEvery
	}
	if signEvery > transport.MaxChatFrameSignEvery {
		signEvery = transport.MaxChatFrameSignEvery
	}
	return &host.ChatFrameSchedule{Join: join, SignEvery: signEvery}
}

// chatFrameJoinFromSchedule is the content-token group for stall budgeting.
// An unset schedule is the executor default.
func chatFrameJoinFromSchedule(schedule *host.ChatFrameSchedule) int {
	if schedule == nil || schedule.Join == 0 {
		return transport.DefaultChatFrameJoin
	}
	join := int(schedule.Join)
	if join > transport.MaxChatFrameJoin {
		return transport.MaxChatFrameJoin
	}
	return join
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
// predates them or a gateway that disabled signing, and the signature checks
// are not logged. The missing finish reason is about the SSE body, so it is
// logged either way.
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
