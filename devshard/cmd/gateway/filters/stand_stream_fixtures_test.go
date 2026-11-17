package filters

import "fmt"

const standPromptTokens = 17

const standChunkFormat = `data: {"choices":[{"delta":{"content":"%c"},"finish_reason":null,"index":0,"logprobs":{"content":[{"bytes":[%d],"logprob":-0.1,"token":"5112696",` +
	`"top_logprobs":[{"bytes":[53,49,49,50,54,57,54],"logprob":-0.1,"token":"5112696"},{"bytes":[54,55,52,53,53,54,51],"logprob":-1.1,"token":"6745563"},` +
	`{"bytes":[53,53,50,51,49,56,50],"logprob":-2.1,"token":"5523182"},{"bytes":[52,51,48,48,56,48,49],"logprob":-3.1,"token":"4300801"},` +
	`{"bytes":[51,48,55,56,52,50,48],"logprob":-4.1,"token":"3078420"}]}]}}],"created":1792336825,"id":"devshard-1-143","model":"model-a","object":"chat.completion.chunk"}` + "\n\n"

var (
	standStreamLengths = []int{64, 512, 2048}
	logprobIntents     = map[string]LogprobIntent{"none": {}, "logprobs": {Keep: true}, "top_logprobs": {Keep: true, KeepTop: true}}
)

func standStream(tokens int) [][]byte {
	events := make([][]byte, 0, tokens+2)
	for index := range tokens {
		letter := 'a' + rune(index%26)
		events = append(events, fmt.Appendf(nil, standChunkFormat, letter, letter))
	}
	events = append(events, fmt.Appendf(nil, `data: {"choices":[],"created":1792336825,"id":"devshard-1-143","model":"model-a","object":"chat.completion.chunk",`+
		`"usage":{"prompt_tokens":17,"completion_tokens":%d,"total_tokens":%d}}`+"\n\n", tokens, standPromptTokens+tokens))
	return append(events, SSEDoneEvent)
}
