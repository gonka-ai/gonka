package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"devshard/internal/boolvalue"

	"common/completionapi"

	devshardpkg "devshard"
	"devshard/internal/e2econfig"
	"devshard/stub"
)

func stubInferenceEngineFromEnv() (devshardpkg.InferenceEngine, error) {
	stubEngine := stub.NewInferenceEngine()
	stubResponseBody, err := e2econfig.StringFromEnv(e2econfig.StubInferenceResponseBodyEnv)
	if err != nil {
		return nil, err
	}
	if stubResponseBody != "" {
		body := []byte(stubResponseBody)
		responseHash := sha256.Sum256(body)
		servedHash, err := stub.HashServedView(body)
		if err != nil {
			return nil, err
		}
		stubEngine.ResponseBody = body
		stubEngine.ResponseHash = responseHash[:]
		stubEngine.ServedHash = servedHash
	}
	return stubEngine, nil
}

type processedStreamEngine struct {
	terminalErrorMessage        string
	logprobsOptimizationEnabled bool
	tamperedStream              bool
}

func (e processedStreamEngine) Execute(_ context.Context, req devshardpkg.ExecuteRequest) (*devshardpkg.ExecuteResult, error) {
	modified, err := completionapi.ModifyRequestBody(req.Prompt, int32(req.InferenceID))
	if err != nil {
		return nil, fmt.Errorf("read the stub request: %w", err)
	}
	processor := completionapi.NewExecutorResponseProcessor(
		fmt.Sprintf("devshard-%s-%d", req.EscrowID, req.InferenceID), modified.AsksForLogprobs)
	processor.SetLogprobsOptimization(req.LogprobsOptimizationOverride, e.logprobsOptimizationEnabled)

	for _, event := range processedStreamEvents(e.terminalErrorMessage) {
		forwarded, err := processor.ProcessStreamedResponse(event)
		if err != nil {
			return nil, fmt.Errorf("process stub event: %w", err)
		}
		if req.ResponseWriter == nil {
			continue
		}
		if e.tamperedStream {
			forwarded = strings.Replace(forwarded, `"content":"hello"`, `"content":"tampered"`, 1)
		}
		_, _ = fmt.Fprintf(req.ResponseWriter, "%s\n\n", forwarded)
		if flusher, ok := req.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
	}

	stored, err := processor.GetResponseBytes()
	if err != nil {
		return nil, fmt.Errorf("collect stub response: %w", err)
	}
	usage, err := processor.GetUsage()
	if err != nil {
		return nil, fmt.Errorf("read stub usage: %w", err)
	}
	servedHash, err := processor.GetServedHash()
	if err != nil {
		return nil, fmt.Errorf("collect stub served view: %w", err)
	}
	responseHash := sha256.Sum256(stored)
	return &devshardpkg.ExecuteResult{
		ResponseHash: responseHash[:],
		ServedHash:   servedHash[:],
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		ResponseBody: stored,
	}, nil
}

func processedStreamEvents(terminalErrorMessage string) []string {
	if terminalErrorMessage != "" {
		return []string{
			`data: {"id":"seed","object":"chat.completion.chunk","created":1,"model":"stub-model",` +
				`"prompt_token_ids":[11,22],"choices":[{"index":0,"delta":{"role":"assistant"},"logprobs":null}],` +
				`"usage":{"prompt_tokens":80,"completion_tokens":0}}`,
			fmt.Sprintf(`data: {"error":{"code":500,"message":%s,"type":"InternalServerError"},"id":"seed"}`,
				strconv.Quote(terminalErrorMessage)),
			"data: [DONE]",
		}
	}
	return []string{
		`data: {"id":"seed","object":"chat.completion.chunk","created":1,"model":"stub-model",` +
			`"prompt_token_ids":[11,22],"choices":[{"index":0,"delta":{"content":"hello"},"token_ids":[123],` +
			`"logprobs":{"content":[{"token":"hello","logprob":-0.5,"bytes":[104,101,108,108,111],` +
			`"top_logprobs":[{"token":"hello","logprob":-0.5,"bytes":[104,101,108,108,111]}]}]}}],` +
			`"usage":{"prompt_tokens":80,"completion_tokens":40}}`,
		"data: [DONE]",
	}
}

func logprobsOptimizationEnabled() bool {
	enabled, err := boolvalue.Parse(os.Getenv("DEVSHARD_LOGPROBS_OPTIMIZATION_ENABLED"))
	if err != nil {
		return false
	}
	return enabled
}
