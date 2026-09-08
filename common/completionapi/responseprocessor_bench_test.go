package completionapi

import (
	"strconv"
	"strings"
	"testing"
)

// A negative count leaves prompt_token_ids out, as return_token_ids off does.
func firstChunk(promptTokens int) string {
	promptField := ""
	if promptTokens >= 0 {
		promptField = `"prompt_token_ids":[` + strings.Repeat(",163586", promptTokens)[min(1, promptTokens):] + `],`
	}
	return `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"moonshotai/Kimi-K2.6",` +
		promptField + `"choices":[{"index":0,"delta":{"role":"assistant"},"logprobs":null}]}`
}

// Only the first chunk grows with the prompt.
func BenchmarkFirstChunk(b *testing.B) {
	for _, promptTokens := range []int{-1, 0, 4_000, 100_000} {
		b.Run("prompt tokens "+strconv.Itoa(promptTokens), func(b *testing.B) {
			chunk := firstChunk(promptTokens)
			b.SetBytes(int64(len(chunk)))
			b.ReportAllocs()
			for b.Loop() {
				processor := NewExecutorResponseProcessor("bench", false)
				if _, err := processor.ProcessStreamedResponse(chunk); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Prices the two ways to answer usage over the same stream.
func BenchmarkExecuteStreamPath(b *testing.B) {
	chunks := append([]string{firstChunk(4_000)}, strings.Split(strings.Repeat(strings.TrimSpace(EVENT)+"\n", 512), "\n")...)
	chunks = append(chunks, `data: {"choices":[],"created":1,"id":"c","model":"moonshotai/Kimi-K2.6",`+
		`"object":"chat.completion.chunk","usage":{"prompt_tokens":4000,"completion_tokens":512,"total_tokens":4512}}`,
		"data: [DONE]")

	streamBytes := 0
	for _, chunk := range chunks {
		streamBytes += len(chunk)
	}

	for _, testCase := range []struct {
		name    string
		collect func(*ExecutorResponseProcessor) error
	}{
		{name: "usage observed on the first pass", collect: usageFromFirstPass},
		{name: "usage from a full re-parse", collect: usageFromReParse},
	} {
		b.Run(testCase.name, func(b *testing.B) {
			b.SetBytes(int64(streamBytes))
			b.ReportAllocs()
			for b.Loop() {
				processor := NewExecutorResponseProcessor("bench", false)
				for _, chunk := range chunks {
					if _, err := processor.ProcessStreamedResponse(chunk); err != nil {
						b.Fatal(err)
					}
				}
				if err := testCase.collect(processor); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func usageFromFirstPass(processor *ExecutorResponseProcessor) error {
	if _, err := processor.GetResponseBytes(); err != nil {
		return err
	}
	_, err := processor.GetUsage()
	return err
}

func usageFromReParse(processor *ExecutorResponseProcessor) error {
	response, err := processor.GetResponse()
	if err != nil {
		return err
	}
	if _, err := response.GetBodyBytes(); err != nil {
		return err
	}
	_, err = response.GetUsage()
	return err
}
