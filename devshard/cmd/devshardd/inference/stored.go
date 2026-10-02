package inference

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"

	"common/completionapi"
	"common/storage/payloads"
	devshardpkg "devshard"
	"devshard/observability"
)

// PayloadReader reads back a payload stored for an inference.
type PayloadReader interface {
	Retrieve(ctx context.Context, escrowId string, inferenceId, epochId uint64) (prompt, response []byte, err error)
}

// storedExecutionResult is the result of an execution whose own response lost
// the store to an earlier execution of the same inference. The caller has
// already relayed its own response, so nothing is written to the client.
func storedExecutionResult(
	ctx context.Context,
	req devshardpkg.ExecuteRequest,
	store PayloadStore,
	payloadEpoch uint64,
) (*devshardpkg.ExecuteResult, error) {
	reader, ok := store.(PayloadReader)
	if !ok {
		return nil, observability.Classify(observability.ReasonPayloadStoreErr, observability.WhereRuntimeExecute,
			fmt.Errorf("store payloads: %w, and the store cannot read it back", payloads.ErrAlreadyStored))
	}
	prompt, response, err := reader.Retrieve(ctx, req.EscrowID, req.InferenceID, payloadEpoch)
	if err != nil {
		return nil, observability.Classify(observability.ReasonPayloadFetchErr, observability.WhereRuntimeExecute, fmt.Errorf("read the payload stored first: %w", err))
	}
	if promptHash := sha256.Sum256(prompt); len(req.PromptHash) > 0 && !bytes.Equal(promptHash[:], req.PromptHash) {
		return nil, observability.Classify(observability.ReasonPayloadFetchErr, observability.WhereRuntimeExecute,
			fmt.Errorf("the payload stored first at epoch %d is for another prompt: expected %x got %x", payloadEpoch, req.PromptHash, promptHash[:]))
	}
	return resultFromStoredResponse(response)
}

// resultFromStoredResponse derives the finish fields from the exact bytes
// validators fetch, the way verifyFetchedPayloadHashes checks them: the
// response hash over the stored bytes, the served hash over their gateway
// view, and the token counts from the stored response.
func resultFromStoredResponse(response []byte) (*devshardpkg.ExecuteResult, error) {
	parsed, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload(response)
	if err != nil {
		return nil, observability.Classify(observability.ReasonProcessResponseErr, observability.WhereRuntimeExecute, fmt.Errorf("parse stored response: %w", err))
	}
	usage, err := parsed.GetUsage()
	if err != nil {
		return nil, observability.Classify(observability.ReasonProcessResponseErr, observability.WhereRuntimeExecute, fmt.Errorf("stored response usage: %w", err))
	}
	served, err := completionapi.StripForGateway(response)
	if err != nil {
		return nil, observability.Classify(observability.ReasonProcessResponseErr, observability.WhereRuntimeExecute, fmt.Errorf("stored response served view: %w", err))
	}
	hash := sha256.Sum256(response)
	servedHash := sha256.Sum256(served)
	return &devshardpkg.ExecuteResult{
		ResponseHash: hash[:],
		ServedHash:   servedHash[:],
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		ResponseBody: response,
	}, nil
}
