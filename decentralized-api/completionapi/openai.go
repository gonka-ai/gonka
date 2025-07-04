package completionapi

type Response struct {
	ID                string   `json:"id"`
	Object            string   `json:"object"`
	Created           int64    `json:"created"`
	Model             string   `json:"model"`
	SystemFingerprint string   `json:"system_fingerprint"`
	Choices           []Choice `json:"choices"`
	Usage             Usage    `json:"usage"`
}

type Choice struct {
	Index    int      `json:"index"`
	Message  *Message `json:"message"`
	Delta    *Delta   `json:"delta"`
	Logprobs struct {
		Content []Logprob `json:"content"`
	} `json:"logprobs"`
	FinishReason string `json:"finish_reason"`
	StopReason   string `json:"stop_reason"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Delta struct {
	Role    *string `json:"role"`
	Content *string `json:"content"`
}

type TopLogprobs struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
	Bytes   []int   `json:"bytes"`
}

type Logprob struct {
	Token       string        `json:"token"`
	Logprob     float64       `json:"logprob"`
	Bytes       []int         `json:"bytes"`
	TopLogprobs []TopLogprobs `json:"top_logprobs"`
}

type Usage struct {
	PromptTokens     uint64 `json:"prompt_tokens"`
	CompletionTokens uint64 `json:"completion_tokens"`
}

func (u *Usage) IsEmpty() bool {
	return u.PromptTokens == 0 && u.CompletionTokens == 0
}

const DataPrefix = "data: "

type SerializedStreamedResponse struct {
	Events []string `json:"events"`
}

type StreamedResponse struct {
	Data []Response `json:"data"`
}
