package public

import (
	cryptotypes "github.com/cometbft/cometbft/proto/tendermint/crypto"
	comettypes "github.com/cometbft/cometbft/types"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
)

type ChatRequest struct {
	Body             []byte
	Request          *http.Request
	OpenAiRequest    OpenAiRequest
	AuthKey          string // signature signing inference request
	PubKey           string // pubkey of participant, who signed inference request
	Seed             string
	InferenceId      string
	RequesterAddress string // address of participant, who signed inference request
}

type OpenAiRequest struct {
	Model     string `json:"model"`
	Seed      int32  `json:"seed"`
	MaxTokens int32  `json:"max_tokens"`
}

type ExecutorDestination struct {
	Url     string `json:"url"`
	Address string `json:"address"`
}

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

type ModelsResponse struct {
	Models []types.Model `json:"models"`
}

type ActiveParticipantWithProof struct {
	ActiveParticipants      types.ActiveParticipants `json:"active_participants"`
	Addresses               []string                 `json:"addresses"`
	ActiveParticipantsBytes string                   `json:"active_participants_bytes"`
	ProofOps                cryptotypes.ProofOps     `json:"proof_ops"`
	Validators              []*comettypes.Validator  `json:"validators"`
	Block                   []*comettypes.Block      `json:"block"`
	// CommitInfo              storetypes.CommitInfo    `json:"commit_info"`
}
