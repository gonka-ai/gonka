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
	Model               string `json:"model"`
	Seed                int32  `json:"seed"`
	MaxTokens           int32  `json:"max_tokens"`
	MaxCompletionTokens int32  `json:"max_completion_tokens"`
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

type ParticipantDto struct {
	Id          string  `json:"id"`
	Url         string  `json:"url"`
	CoinsOwed   int64   `json:"coins_owed"`
	RefundsOwed int64   `json:"refunds_owed"`
	Balance     int64   `json:"balance"`
	VotingPower int64   `json:"voting_power"`
	Reputation  float32 `json:"reputation"`
}

type ParticipantsDto struct {
	Participants []ParticipantDto `json:"participants"`
	BlockHeight  int64            `json:"block_height"`
}

type StartTrainingDto struct {
	HardwareResources []HardwareResourcesDto `json:"hardware_resources"`
	Config            TrainingConfigDto      `json:"config"`
}

type HardwareResourcesDto struct {
	Type  string `json:"type"`
	Count uint32 `json:"count"`
}

type TrainingConfigDto struct {
	Datasets              TrainingDatasetsDto `json:"datasets"`
	NumUocEstimationSteps uint32              `json:"num_uoc_estimation_steps"`
}

type TrainingDatasetsDto struct {
	Train string `json:"train"`
	Test  string `json:"test"`
}

type LockTrainingNodesDto struct {
	TrainingTaskId uint64   `json:"training_task_id"`
	NodeIds        []string `json:"node_ids"`
}

type ProofVerificationRequest struct {
	Value    string               `json:"value"`
	AppHash  string               `json:"app_hash"`
	ProofOps cryptotypes.ProofOps `json:"proof_ops"`
	Epoch    int64                `json:"epoch"`
}

type VerifyBlockRequest struct {
	Block      comettypes.Block `json:"block"`
	Validators []Validator      `json:"validators"`
}

type Validator struct {
	PubKey      string `json:"pub_key"`
	VotingPower int64  `json:"voting_power"`
}

type SubmitUnfundedNewParticipantDto struct {
	Address      string `json:"address"`
	Url          string `json:"url"`
	ValidatorKey string `json:"validator_key"`
	PubKey       string `json:"pub_key"`
	WorkerKey    string `json:"worker_key"`
}

type UnitOfComputePriceProposalDto struct {
	Price uint64 `json:"price"`
	Denom string `json:"denom"`
}

type PricingDto struct {
	Price  uint64          `json:"unit_of_compute_price"`
	Models []ModelPriceDto `json:"models"`
}

type RegisterModelDto struct {
	Id                     string `json:"id"`
	UnitsOfComputePerToken uint64 `json:"units_of_compute_per_token"`
}

type ModelPriceDto struct {
	Id                     string `json:"id"`
	UnitsOfComputePerToken uint64 `json:"units_of_compute_per_token"`
	PricePerToken          uint64 `json:"price_per_token"`
}

// FinalizedBlock represents a finalized block with optional receipts
type BridgeBlock struct {
	BlockNumber  string          `json:"blockNumber"`
	OriginChain  string          `json:"originChain"`        // Name of the origin chain (e.g., "ethereum")
	ReceiptsRoot string          `json:"receiptsRoot"`       // Merkle root of receipts trie for transaction verification
	Receipts     []BridgeReceipt `json:"receipts,omitempty"` // Optional list of receipts
}
type BridgeReceipt struct {
	ContractAddress string `json:"contract"`     // Address of the smart contract on the origin chain
	OwnerAddress    string `json:"owner"`        // Address of the token owner on the origin chain
	OwnerPubKey     string `json:"publicKey"`    // Public key of the token owner on the origin chain
	Amount          string `json:"amount"`       // Amount of tokens to be bridged
	ReceiptIndex    string `json:"receiptIndex"` // Index of the transaction receipt in the block
}
