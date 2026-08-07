package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"devshard/accounting"
	"devshard/observability"
	"devshard/transport"
	"devshard/user"
)

// QuarantinePayloadStats carries size/attr evidence into quarantine transitions
// without exposing request bodies to the limiter.
type QuarantinePayloadStats struct {
	Ctx           context.Context
	RequestBytes  int
	ResponseBytes int
	Stream        bool
	MessageCount  int
	MaxTokens     uint64
}

// maybeLogMLNodePayloadForAttempt emits a payload_captured line at most once per
// attempt when the failure is host_response. Called from send-path classifiers
// (empty/error/truncated) and again from recordGatewayAttemptTerminal.
func (e *Redundancy) maybeLogMLNodePayloadForAttempt(inf *inflight, params user.InferenceParams, origin accounting.FailureOrigin, detail string) {
	if e == nil || inf == nil || inf.probe {
		return
	}
	if origin != accounting.FailureHostResponse {
		return
	}
	if !observability.LoadPayloadPolicy().MLNodeCaptureEnabled() {
		return
	}
	if !inf.payloadCaptured.CompareAndSwap(false, true) {
		return
	}
	maybeLogMLNodePayload(inf.attemptCtx(), inf, params, origin, detail)
}

func maybeLogMLNodePayload(ctx context.Context, inf *inflight, params user.InferenceParams, origin accounting.FailureOrigin, detail string) {
	policy := observability.LoadPayloadPolicy()
	if !policy.MLNodeCaptureEnabled() {
		return
	}
	if origin != accounting.FailureHostResponse {
		return
	}
	if inf == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	failedAt := time.Now()
	hash := observability.PromptSHA256(params.Prompt)
	body, bodyTrunc := collectFailureResponseBody(inf)
	fields := observability.FormatPayloadBodies(policy.Level, policy.MaxBytes, params.Prompt, body)
	fields = append(fields,
		"model", gatewayMetricModel(params, ""),
		"max_tokens", params.MaxTokens,
		"stream", params.Stream,
		"message_count", observability.CountChatMessages(params.Prompt),
		"detail_reason", detail,
		"failure_origin", observability.FailureOriginString(origin),
		"failed_at", failedAt.UTC().Format(time.RFC3339Nano),
	)
	if bodyTrunc {
		fields = append(fields, "response_truncated", true)
	}
	if !inf.sendTime.IsZero() {
		fields = append(fields,
			"sent_at", inf.sendTime.UTC().Format(time.RFC3339Nano),
			"response_ms", failedAt.Sub(inf.sendTime).Milliseconds(),
		)
	}
	if ft := inf.firstTokenAt(); !ft.IsZero() && !inf.sendTime.IsZero() {
		fields = append(fields, "first_token_ms", ft.Sub(inf.sendTime).Milliseconds())
	}

	status, headers := collectFailureResponseMeta(inf)
	if status > 0 {
		fields = append(fields, "http_status", status)
	}
	if len(headers) > 0 {
		if raw, err := json.Marshal(headers); err == nil {
			fields = append(fields, "response_headers", string(raw))
		}
	}
	if inf.errorSource != "" {
		fields = append(fields,
			"error_source", inf.errorSource,
			"error_code", inf.errorCode,
			"error_type", inf.errorType,
			"error_message", inf.errorMessage,
		)
	}
	fields = append(fields,
		"content_chunks", inf.contentChunks.Load(),
		"output_chunks", inf.outputChunks.Load(),
		"output_bytes", inf.outputBytes.Load(),
		"has_receipt", !inf.receiptAt().IsZero(),
	)
	if inf.resp != nil {
		fields = append(fields, "stream_bytes_read", inf.resp.StreamBytesRead)
	}

	logCtx := inf.attemptCtx()
	if logCtx == nil {
		logCtx = ctx
	}
	logInferenceStage(logCtx, inf.escrowID, inf.nonce, "payload_captured", fields...)
	observability.AddPayloadCaptured(inf.eventSpan(), hash)
}

func collectFailureResponseBody(inf *inflight) ([]byte, bool) {
	if inf == nil {
		return nil, false
	}
	var ue *transport.UpstreamStatusError
	if errors.As(inf.err, &ue) && ue != nil && ue.Body != "" {
		return []byte(ue.Body), false
	}
	if len(inf.errorBodySample) > 0 {
		return append([]byte(nil), inf.errorBodySample...), true
	}
	if inf.emptyResponseBodySample != "" {
		return []byte(inf.emptyResponseBodySample), inf.emptyResponseBodySampleTruncated
	}
	if len(inf.payloadResponseSample) > 0 {
		return append([]byte(nil), inf.payloadResponseSample...), inf.payloadResponseSampleTruncated
	}
	if len(inf.shortContentResponseBody) > 0 {
		return append([]byte(nil), inf.shortContentResponseBody...), inf.shortContentResponseBodyTruncated
	}
	if len(inf.pendingBuf) > 0 {
		return append([]byte(nil), inf.pendingBuf...), false
	}
	return nil, false
}

func collectFailureResponseMeta(inf *inflight) (int, map[string]string) {
	if inf == nil {
		return 0, nil
	}
	var ue *transport.UpstreamStatusError
	if errors.As(inf.err, &ue) && ue != nil {
		return ue.StatusCode, ue.Headers
	}
	// Streaming host answer that later failed still had HTTP 200.
	if inf.err != nil || inf.errorSource != "" || isEmptyStreamAttempt(inf) {
		return httpStatusOKOrZero(inf), nil
	}
	return 0, nil
}

func httpStatusOKOrZero(inf *inflight) int {
	if inf == nil {
		return 0
	}
	// Host answered (receipt / content / empty-stream classify) ⇒ HTTP 200 to gateway.
	if inf.err == errEmptyStream || isEmptyStreamAttempt(inf) || inf.errorSource != "" ||
		len(inf.payloadResponseSample) > 0 || errors.Is(inf.err, transport.ErrSSEStreamTruncated) {
		return 200
	}
	return 0
}

func maybeLogQuarantinePayload(participantKey, modelID, mode, reason string, stats QuarantinePayloadStats) {
	policy := observability.LoadPayloadPolicy()
	if !policy.QuarantineCaptureEnabled() {
		return
	}
	ctx := stats.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	fields := []any{
		"participant_key", participantKey,
		"model_id", modelID,
		"quarantine_mode", mode,
		"reason", reason,
		"request_bytes", stats.RequestBytes,
		"response_bytes", stats.ResponseBytes,
		"stream", stats.Stream,
		"message_count", stats.MessageCount,
		"max_tokens", stats.MaxTokens,
		"failed_at", time.Now().UTC().Format(time.RFC3339Nano),
	}
	logRequestStage(ctx, "payload_quarantine", fields...)
}
