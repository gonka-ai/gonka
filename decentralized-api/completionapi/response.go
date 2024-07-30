package completionapi

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

type Message struct {
	Role      string   `json:"role"`
	Content   string   `json:"content"`
	ToolCalls []string `json:"tool_calls"`
}

type Choice struct {
	Index    int     `json:"index"`
	Message  Message `json:"message"`
	Logprobs struct {
		Content []Logprob `json:"content"`
	} `json:"logprobs"`
	FinishReason string  `json:"finish_reason"`
	StopReason   *string `json:"stop_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type Response struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}
