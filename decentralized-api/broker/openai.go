package broker

// Define the structs that match the JSON structure
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
	Index    int     `json:"index"`
	Message  Message `json:"message"`
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

type Logprob struct {
	Token       string  `json:"token"`
	Logprob     float64 `json:"logprob"`
	Bytes       []int   `json:"bytes"`
	TopLogprobs []struct {
		Token   string  `json:"token"`
		Logprob float64 `json:"logprob"`
		Bytes   []int   `json:"bytes"`
	} `json:"top_logprobs"`
}

type Usage struct {
	PromptTokens     uint64 `json:"prompt_tokens"`
	CompletionTokens uint64 `json:"completion_tokens"`
	TotalTokens      uint64 `json:"total_tokens"`
}
