package inference

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"common/completionapi"
)

const benchVLLMDeltaLine = `data: {"id":"chatcmpl-5f2c","object":"chat.completion.chunk","created":1790000000,"model":"moonshotai/Kimi-K2.6","choices":[{"index":0,"delta":{"content":" user"},"logprobs":{"content":[{"token":"3100","logprob":-0.0123,"bytes":[51,49,48,48],"top_logprobs":[{"token":"3100","logprob":-0.0123,"bytes":[51,49,48,48]},{"token":"3101","logprob":-4.51,"bytes":[51,49,48,49]},{"token":"77","logprob":-6.2,"bytes":[55,55]},{"token":"912","logprob":-7.9,"bytes":[57,49,50]},{"token":"4410","logprob":-8.3,"bytes":[52,52,49,48]}]}]},"finish_reason":null,"token_ids":[3100]}]}`

type flushCountingWriter struct {
	discardWriter
	flushes int
}

func (writer *flushCountingWriter) Flush() { writer.flushes++ }

func benchmarkExecutorStream(b *testing.B, optimizeLogprobs bool) {
	stream := strings.Repeat(benchVLLMDeltaLine+"\n\n", 200) + "data: [DONE]\n\n"
	b.ReportAllocs()
	flushes := 0
	for b.Loop() {
		writer := &flushCountingWriter{discardWriter: discardWriter{header: http.Header{}}}
		processor := completionapi.NewExecutorResponseProcessor("inf-bench", false)
		processor.SetLogprobsOptimization(nil, optimizeLogprobs)
		response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(stream))}
		if err := proxyTextStreamResponse(response, writer, processor, "inf-bench"); err != nil {
			b.Fatal(err)
		}
		flushes = writer.flushes
	}
	b.ReportMetric(float64(flushes), "flushes/stream")
}

func BenchmarkExecutorStreamOptimizationOff(b *testing.B) { benchmarkExecutorStream(b, false) }

func BenchmarkExecutorStreamOptimizationOn(b *testing.B) { benchmarkExecutorStream(b, true) }
