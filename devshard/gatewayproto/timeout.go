package gatewayproto

import (
	"context"
	"time"

	"devshard/host"
	"devshard/types"
	"devshard/user"
)

// TimeoutHandler is the existing protocol core's resolution capability.
type TimeoutHandler interface {
	HandleTimeout(context.Context, uint64, time.Time, *host.InferencePayload) (user.TimeoutResult, error)
	HandleErrorMiss(context.Context, uint64, []byte, []byte) (user.TimeoutResult, error)
}

// MissClaim carries the signed Finish and the original response evidence. The
// adapter chooses the Finish source; resolving does not repeat that lookup.
type MissClaim struct{ FinishTx, ResponsePayload []byte }

// HandleProtocolTimeout preserves both the core's complete outcome and its
// error, including an applied transition alongside a delivery failure.
func HandleProtocolTimeout(ctx context.Context, handler TimeoutHandler, nonce uint64, sendTime time.Time, payload *host.InferencePayload, claim *MissClaim) (user.TimeoutResult, error) {
	if claim != nil && len(claim.FinishTx) > 0 {
		return handler.HandleErrorMiss(ctx, nonce, claim.FinishTx, claim.ResponsePayload)
	}
	return handler.HandleTimeout(ctx, nonce, sendTime, payload)
}

func TimeoutPayload(params user.InferenceParams) *host.InferencePayload {
	return &host.InferencePayload{Prompt: params.Prompt, Model: params.Model, InputLength: params.InputLength, MaxTokens: params.MaxTokens, StartedAt: params.StartedAt}
}

func TimeoutPayloadForRecord(record types.InferenceRecord, prompt []byte) *host.InferencePayload {
	return &host.InferencePayload{Prompt: prompt, Model: record.Model, InputLength: record.InputLength, MaxTokens: record.MaxTokens, StartedAt: record.StartedAt}
}
