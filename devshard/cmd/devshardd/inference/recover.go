package inference

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"common/completionapi"
	"common/storage/payloads"
	devshardpkg "devshard"
	"devshard/observability"
)

// recoverStoredExecution rebuilds an execution result from a response an
// earlier execution of this inference already stored. A nil result with a
// nil error means nothing is stored at any candidate epoch.
//
// The result reproduces the lost finish: the hash covers the exact bytes
// validators fetch, and the token counts come from the parser validators use.
func recoverStoredExecution(
	ctx context.Context,
	req devshardpkg.ExecuteRequest,
	reader PayloadReader,
	phaseEpoch uint64,
) (*devshardpkg.ExecuteResult, error) {
	if reader == nil {
		return nil, nil
	}
	prompt, response, epoch, err := retrieveStoredPayload(ctx, reader, req, recoveryEpochs(req.EpochID, phaseEpoch))
	if err != nil {
		return nil, observability.Classify(observability.ReasonPayloadFetchErr, observability.WhereRuntimeExecute, fmt.Errorf("read stored payload: %w", err))
	}
	if response == nil {
		return nil, nil
	}
	if promptHash := sha256.Sum256(prompt); len(req.PromptHash) > 0 && !bytes.Equal(promptHash[:], req.PromptHash) {
		return nil, observability.Classify(observability.ReasonPayloadFetchErr, observability.WhereRuntimeExecute,
			fmt.Errorf("stored prompt at epoch %d does not match the inference: expected %x got %x", epoch, req.PromptHash, promptHash[:]))
	}
	result, err := resultFromStoredResponse(response)
	if err != nil {
		return nil, err
	}
	if req.ResponseWriter != nil {
		if err := writeStoredResponse(req.ResponseWriter, response); err != nil {
			return nil, fmt.Errorf("relay stored response: %w", err)
		}
	}
	return result, nil
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

// resultFromStoredResponse hashes the exact bytes validators fetch and takes
// the token counts from the parser validators use.
func resultFromStoredResponse(response []byte) (*devshardpkg.ExecuteResult, error) {
	parsed, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload(response)
	if err != nil {
		return nil, observability.Classify(observability.ReasonProcessResponseErr, observability.WhereRuntimeExecute, fmt.Errorf("parse stored response: %w", err))
	}
	usage, err := parsed.GetUsage()
	if err != nil {
		return nil, observability.Classify(observability.ReasonProcessResponseErr, observability.WhereRuntimeExecute, fmt.Errorf("stored response usage: %w", err))
	}
	hash := sha256.Sum256(response)
	return &devshardpkg.ExecuteResult{
		ResponseHash: hash[:],
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		ResponseBody: response,
	}, nil
}

// recoveryEpochs lists the epochs a stored payload can sit under. Execution
// stores under the phase epoch at the time, which is usually the escrow epoch
// or the one after it.
func recoveryEpochs(escrowEpoch, phaseEpoch uint64) []uint64 {
	out := make([]uint64, 0, 4)
	add := func(epoch uint64) {
		for _, seen := range out {
			if seen == epoch {
				return
			}
		}
		out = append(out, epoch)
	}
	add(escrowEpoch)
	add(escrowEpoch + 1)
	if phaseEpoch > 0 {
		add(phaseEpoch)
	}
	if escrowEpoch > 0 {
		add(escrowEpoch - 1)
	}
	return out
}

func retrieveStoredPayload(ctx context.Context, reader PayloadReader, req devshardpkg.ExecuteRequest, epochs []uint64) ([]byte, []byte, uint64, error) {
	for _, epoch := range epochs {
		prompt, response, err := reader.Retrieve(ctx, req.EscrowID, req.InferenceID, epoch)
		if errors.Is(err, payloads.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, nil, 0, err
		}
		if len(response) > 0 {
			return prompt, response, epoch, nil
		}
	}
	return nil, nil, 0, nil
}

// writeStoredResponse sends a stored response as SSE. A streamed response is
// stored as its event lines and is replayed line by line; a JSON response is
// one data event.
func writeStoredResponse(w http.ResponseWriter, response []byte) error {
	var streamed completionapi.SerializedStreamedResponse
	if err := json.Unmarshal(response, &streamed); err != nil || streamed.Events == nil {
		if _, err := fmt.Fprintf(w, "%s%s\n\n", completionapi.DataPrefix, response); err != nil {
			return err
		}
		return writeSSEDone(w)
	}
	done := false
	for _, line := range streamed.Events {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == completionapi.DataPrefix+"[DONE]" {
			done = true
		}
		if _, err := fmt.Fprintf(w, "%s\n\n", line); err != nil {
			return err
		}
	}
	if done {
		flushWriter(w)
		return nil
	}
	return writeSSEDone(w)
}

func writeSSEDone(w http.ResponseWriter) error {
	if _, err := fmt.Fprintf(w, "%s[DONE]\n\n", completionapi.DataPrefix); err != nil {
		return err
	}
	flushWriter(w)
	return nil
}

func flushWriter(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
