package transport

import (
	"context"
	"io"
	"strings"
	"testing"
)

const benchDeltaLine = `data: {"id":"chatcmpl-5f2c","object":"chat.completion.chunk","created":1790000000,"model":"moonshotai/Kimi-K2.6","choices":[{"index":0,"delta":{"content":" user"},"logprobs":{"content":[{"token":" user","logprob":-0.0123,"bytes":[32,117,115,101,114],"top_logprobs":[{"token":" user","logprob":-0.0123,"bytes":[32,117,115,101,114]},{"token":" users","logprob":-4.51,"bytes":[32,117,115,101,114,115]},{"token":" client","logprob":-6.2,"bytes":[32,99,108,105,101,110,116]},{"token":" person","logprob":-7.9,"bytes":[32,112,101,114,115,111,110]},{"token":" customer","logprob":-8.3,"bytes":[32,99,117,115,116,111,109,101,114]}]}]},"finish_reason":null,"token_ids":[3100]}]}`

// BenchmarkParseSSEResponse is one 200-token answer from receipt to meta tail, forwarded to a discarding writer.
func BenchmarkParseSSEResponse(b *testing.B) {
	stream := `data: {"devshard_receipt":{"nonce":1}}` + "\n\n" + strings.Repeat(benchDeltaLine+"\n\n", 200) + "data: [DONE]\n\n" + `data: {"devshard_meta":{}}` + "\n\n"
	client := streamBoundClient(DefaultMaxSSEStreamBytes)
	b.SetBytes(int64(len(stream)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := client.parseSSEResponse(context.Background(), strings.NewReader(stream), io.Discard, nil); err != nil {
			b.Fatal(err)
		}
	}
}
