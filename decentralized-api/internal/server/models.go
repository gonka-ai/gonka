package server

import "net/http"

type InferenceTransaction struct {
	PromptHash           string `json:"promptHash"`
	PromptPayload        string `json:"promptPayload"`
	ResponseHash         string `json:"responseHash"`
	ResponsePayload      string `json:"responsePayload"`
	PromptTokenCount     uint64 `json:"promptTokenCount"`
	CompletionTokenCount uint64 `json:"completionTokenCount"`
	Model                string `json:"model"`
	Id                   string `json:"id"`
}

type ChatRequest struct {
	Body                 []byte
	Request              *http.Request
	OpenAiRequest        OpenAiRequest
	AuthKey              string
	PubKey               string
	Seed                 string
	InferenceId          string
	RequesterAddress     string
	FundedByTransferNode bool
}

// Only extract info we need
type OpenAiRequest struct {
	Model     string `json:"model"`
	Seed      int32  `json:"seed"`
	MaxTokens int32  `json:"max_tokens"`
}

type ExecutorDestination struct {
	Url     string `json:"url"`
	Address string `json:"address"`
}

// Debug-only request
type ValidationRequest struct {
	Id string `json:"id"`
}

type ParticipantsDto struct {
	Participants []ParticipantDto `json:"participants"`
	BlockHeight  int64            `json:"block_height"`
}

type ParticipantDto struct {
	Id          string   `json:"id"`
	Url         string   `json:"url"`
	Models      []string `json:"models"`
	CoinsOwed   int64    `json:"coins_owed"`
	RefundsOwed int64    `json:"refunds_owed"`
	Balance     int64    `json:"balance"`
	VotingPower int64    `json:"voting_power"`
	Reputation  float32  `json:"reputation"`
}

type ResponseWithBody struct {
	Response  *http.Response
	BodyBytes []byte
}
