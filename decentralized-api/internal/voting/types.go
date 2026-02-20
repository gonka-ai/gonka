// Package voting provides types and services for the node voting mechanism.
// This mechanism allows nodes to dispute another node's (the Respondent's) behavior
// by initiating a vote among sampled nodes.
package voting

// DefaultMaxVoters is the default number of voters to sample for fallback verification.
const DefaultMaxVoters = 5

// ChallengerRole identifies who is initiating the dispute.
// The role determines what proof is required and what verification voters perform.
type ChallengerRole int

const (
	// RoleExecutor indicates an executor is challenging a TA for not sending the payload.
	// Proof: On-chain MsgStartInference.assigned_to == ChallengerAddress
	// No ChallengerDataHash needed (executor has nothing to provide).
	RoleExecutor ChallengerRole = iota

	// RoleTA indicates a TA is challenging an executor for not finishing the inference.
	// Proof: On-chain MsgStartInference.creator == ChallengerAddress
	// ChallengerDataHash should contain the hash of the prompt_payload TA sent.
	RoleTA
)

// Returns a string representation of the ChallengerRole.
func (r ChallengerRole) String() string {
	switch r {
	case RoleExecutor:
		return "executor"
	case RoleTA:
		return "ta"
	default:
		return "unknown"
	}
}

// IsValid returns true if the ChallengerRole is a recognized value.
func (r ChallengerRole) IsValid() bool {
	return r == RoleExecutor || r == RoleTA
}

// AssignmentProof contains the minimum data from ChatRequest to prove an inference was assigned.
// If the signature verifies, it proves TA assigned this inference to this executor.
type AssignmentProof struct {
	// InferenceId is the unique identifier of the inference.
	InferenceId string `json:"inference_id"`

	// TransferAddress is the TA's address who assigned the inference.
	TransferAddress string `json:"transfer_address"`

	// ExecutorAddress is the address of the node assigned to execute.
	ExecutorAddress string `json:"executor_address"`

	// TransferSignature is the TA's signature proving assignment (KEY PROOF).
	// Signs: prompt_hash + timestamp + transfer_addr + executor_addr
	TransferSignature string `json:"transfer_signature"`

	// PromptHash is the hash of the prompt payload that was signed.
	PromptHash string `json:"prompt_hash"`

	// Timestamp is the Unix timestamp (in nanoseconds) when the assignment was made.
	Timestamp int64 `json:"timestamp"`

	// AuthKey is the developer's signature for authorization verification (optional).
	AuthKey string `json:"auth_key,omitempty"`

	// RequesterAddress is the developer's address who requested the inference (optional).
	RequesterAddress string `json:"requester_address,omitempty"`

	// Seed is used for deterministic verification (optional).
	Seed [32]byte `json:"seed,omitempty"`
}

// VerificationType specifies what voters should verify during the voting process.
//
// Before casting a negative vote, voters must attempt recovery:
// 1. If data is missing, voter tries to fetch it from the respondent
// 2. If successful, voter relays the data to the challenger (giving them a chance to receive it)
// 3. Only if data is still unavailable after retries, cast a negative vote
type VerificationType int

const (

	// VerifyPayloadFromTA indicates voters should ping the TA's payload endpoint.
	// Used when an executor challenges a TA for not sending the payload.
	// Recovery: If missing, voter requests payload from TA and relays to executor if found.
	// Voters will: GET /v1/inference/payloads?inference_id=... on TA's endpoint
	VerifyPayloadFromTA VerificationType = iota

	// VerifyPayloadFromExecutor indicates voters should ping the executor's payload endpoint.
	// Used when a TA challenges an executor for not storing the response payload.
	// Recovery: If missing, voter requests payload from executor and relays to TA if found.
	// Voters will: GET /v1/inference/payloads?inference_id=... on executor's endpoint
	VerifyPayloadFromExecutor

	// VerifyMsgStartExists indicates voters should check if MsgStartInference exists on-chain.
	// Used when an executor challenges a TA for not posting MsgStartInference.
	// Recovery: If missing on-chain, voter requests TA to post it, then re-checks.
	// Voters will: Query chain for MsgStartInference with the inference_id.
	VerifyMsgStartExists

	// VerifyMsgFinishExists indicates voters should check if MsgFinishInference exists on-chain.
	// Used when a TA challenges an executor for not completing the inference.
	// Recovery: If missing on-chain, voter requests executor to post it, then re-checks.
	// Voters will: Query chain for MsgFinishInference with the inference_id.
	VerifyMsgFinishExists

	// VerifyPromptHashMatch indicates voters should verify the TA's payload hash matches on-chain.
	// Used when an executor challenges a TA for payload mismatch.
	// Voters will: Compare TA's payload hash vs MsgStartInference.prompt_hash
	VerifyPromptHashMatch

	// VerifyResponseHashMatch indicates voters should verify the executor's response hash matches on-chain.
	// Used when a TA challenges an executor for response hash mismatch.
	// Voters will: Compare executor's payload hash vs MsgFinishInference.response_hash
	VerifyResponseHashMatch

	// VerifyResponseDeliveryToTA indicates voters should verify the TA received the response from executor.
	// Used when a TA claims they never received the HTTP response from executor.
	// Recovery: Voter fetches response from executor's payload endpoint and relays to TA.
	// Voters will:
	//   1. Check if MsgFinishInference exists (executor completed)
	//   2. Fetch response payload from executor
	//   3. Relay response to TA's callback endpoint
	//   4. Vote negative if executor doesn't have it
	VerifyResponseDeliveryToTA
)

// Returns a string representation of the VerificationType.
func (v VerificationType) String() string {
	switch v {
	case VerifyPayloadFromTA:
		return "payload_from_ta"
	case VerifyPayloadFromExecutor:
		return "payload_from_executor"
	case VerifyMsgStartExists:
		return "msg_start_exists"
	case VerifyMsgFinishExists:
		return "msg_finish_exists"
	case VerifyPromptHashMatch:
		return "prompt_hash_match"
	case VerifyResponseHashMatch:
		return "response_hash_match"
	case VerifyResponseDeliveryToTA:
		return "response_delivery_to_ta"
	default:
		return "unknown"
	}
}

// IsValid returns true if the VerificationType is a recognized value.
func (v VerificationType) IsValid() bool {
	switch v {
	case VerifyPayloadFromTA,
		VerifyPayloadFromExecutor,
		VerifyMsgStartExists,
		VerifyMsgFinishExists,
		VerifyPromptHashMatch,
		VerifyResponseHashMatch,
		VerifyResponseDeliveryToTA:
		return true
	default:
		return false
	}
}

// VoteType represents the outcome of a node's verification of Respondent behavior.
type VoteType uint16

const (
	// VotePositive indicates the Respondent responded honestly with valid data.
	VotePositive VoteType = iota
	// VoteNegative indicates the Respondent did not respond or was dishonest.
	VoteNegative
	// VoteInvalid indicates the vote could not be determined (error case).
	VoteInvalid
)

// Returns a string representation of the VoteType.
func (v VoteType) String() string {
	switch v {
	case VotePositive:
		return "positive"
	case VoteNegative:
		return "negative"
	case VoteInvalid:
		return "invalid"
	default:
		return "unknown"
	}
}

// IsValid returns true if the VoteType is a recognized value.
func (v VoteType) IsValid() bool {
	return v == VotePositive || v == VoteNegative || v == VoteInvalid
}

// VotingOutcome represents the final result of all votes.
type VotingOutcome int

const (
	// OutcomePositive indicates that at least one node voted positively for the inference and got the payload.
	OutcomePositive VotingOutcome = iota
	// OutcomeNegative indicates that at all nodes voted negatively for the inference and did not get the payload.
	OutcomeNegative
	// OutcomeInconclusive for cases, when the voting went wrong
	OutcomeInconclusive
)

// Returns a string representation of the VotingOutcome.
func (o VotingOutcome) String() string {
	switch o {
	case OutcomePositive:
		return "positive"
	case OutcomeNegative:
		return "negative"
	case OutcomeInconclusive:
		return "inconclusive"
	default:
		return "unknown"
	}
}

// IsValid returns true if the VotingOutcome is a recognized value.
func (o VotingOutcome) IsValid() bool {
	return o == OutcomePositive || o == OutcomeNegative || o == OutcomeInconclusive
}

// SignedVote contains a node's vote with cryptographic proof.
// Each vote is signed by the voting node to ensure authenticity and prevent tampering.
type SignedVote struct {
	// ID of the inference being disputed.
	InferenceId string `json:"inference_id"`

	// VoterAddress is the blockchain address of the node casting the vote.
	// Will be used to verify the signature of the vote.
	VoterAddress string `json:"voter_address"`

	// VoteType indicates the node's assessment of the Respondent's behavior.
	VoteType VoteType `json:"vote_type"`

	// RespondentDataHash is the signed hash of data the Respondent provided (if any).
	// Empty if the Respondent did not respond.
	RespondentDataHash string `json:"respondent_data_hash,omitempty"`

	// Timestamp is the Unix timestamp when the vote was cast.
	Timestamp int64 `json:"timestamp"`

	// VoterSignature is the cryptographic signature of the vote.
	// Signs: inference_id + voter_address + vote_type + respondent_data_hash + timestamp
	// Used for on-chain verification of vote authenticity.
	VoterSignature string `json:"voter_signature"`
}

// VoteResponse represents a node's response to a verification request.
type VoteResponse struct {
	// Vote is the signed vote from the node.
	Vote SignedVote `json:"vote"`

	// Error is any error that occurred during verification.
	// If set, the vote may not be valid.
	Error string `json:"error,omitempty"`
}

// VotingInitiateRequest represents a request from a Challenger to initiate a vote/dispute against a Respondent.
//
// VerificationType determines how voters should verify the disputed inference.
//
// Voters may combine on-chain verification and off-chain checks (e.g., payload availability).
type VotingInitiateRequest struct {
	InferenceId string `json:"inference_id"`

	// ChallengerAddress is the address of the node initiating the dispute.
	ChallengerAddress string `json:"challenger_address"`

	// ChallengerRole identifies who is initiating the dispute.
	// Determines what proof is required and what verification voters perform.
	ChallengerRole ChallengerRole `json:"challenger_role"`

	// VerificationType specifies what kind of verification voters should perform.
	// This is required so that all voters know exactly what check to do,
	// and it allows disambiguation between dispute types (assignment, payload, completion, etc).
	VerificationType VerificationType `json:"verification_type"`

	// RespondentAddress is the address of the node being disputed.
	RespondentAddress string `json:"respondent_address"`

	// AssignmentProof contains cryptographic proof of the inference assignment.
	// The TransferSignature alone proves TA assigned this inference to the executor.
	AssignmentProof *AssignmentProof `json:"assignment_proof,omitempty"`

	// Reason describes why the Challenger is disputing the Respondent.
	Reason string `json:"reason,omitempty"`

	// ChallengerSignature is the Challenger's signature on this request.
	// Signs: inference_id + challenger_address + respondent_address + timestamp
	ChallengerSignature string `json:"challenger_signature"`

	// Timestamp is when the dispute was initiated (Unix timestamp in nanoseconds).
	Timestamp int64 `json:"timestamp"`
}

// VotingResult represents the aggregated result of a voting session.
// TODO! Use this for negative or failed voting cases.
type VotingResult struct {
	// ID of the inference that was disputed.
	InferenceId string `json:"inference_id"`

	// Votes contains all the signed votes collected.
	Votes []SignedVote `json:"votes"`

	// Outcome is the aggregated voting outcome.
	Outcome VotingOutcome `json:"outcome"`

	// NegativeCount is the number of negative votes.
	NegativeCount int `json:"negative_count"`

	// InvalidCount is the number of invalid votes (Respondent served invalid data).
	InvalidCount int `json:"invalid_count"`

	// CompletedAt is the timestamp when voting completed.
	CompletedAt int64 `json:"completed_at"`
}

// VoteRequest represents a request sent to a node by a voting coordinator to verify Respondent behavior.
type VoteRequest struct {
	// ID of the inference to verify.
	InferenceId string `json:"inference_id"`

	// RespondentAddress is the address of the Respondent to verify.
	RespondentAddress string `json:"respondent_address"`

	// VerificationType specifies what type of verification voters should perform.
	VerificationType VerificationType `json:"verification_type"`

	// ExpectedDataHash is the expected hash of data from challenger or chain.
	ExpectedDataHash string `json:"expected_data_hash,omitempty"`

	// RequestTimestamp is when the verification request was initiated.
	RequestTimestamp int64 `json:"request_timestamp"`

	// RequesterAddress is the address of the node requesting the verification (voting coordinator).
	RequesterAddress string `json:"requester_address"`

	// RequesterSignature proves this request is from a legitimate voting coordinator.
	// Signs: inference_id + respondent_address + verification_type + request_timestamp
	RequesterSignature string `json:"requester_signature"`
}

// OnChainProof contains data fetched from chain (MsgStartInference and MsgFinishInference) to validate challenger claims.
// This is used by voters to verify that the challenger has a legitimate dispute.
type OnChainProof struct {
	// InferenceExists indicates whether MsgStartInference exist for this inference_id.
	InferenceExists bool `json:"inference_exists"`

	// AssignedTo is the executor address from MsgStartInference.assigned_to.
	// Used to verify executor challenger: MsgStartInference.assigned_to == ChallengerAddress
	AssignedTo string `json:"assigned_to"`

	// CreatedBy is the TA address from MsgStartInference.creator.
	// Used to verify TA challenger: MsgStartInference.creator == ChallengerAddress
	CreatedBy string `json:"created_by"`

	// FinishExists indicates whether MsgFinishInference exists for this inference_id.
	// If false and TA is challenger, executor failed to complete.
	FinishExists bool `json:"finish_exists"`

	// ExpectedPromptHash is the prompt_hash from MsgStartInference.
	// Used to verify data served by TA matches what was committed on-chain.
	ExpectedPromptHash string `json:"expected_prompt_hash"`

	// ExpectedResponseHash is the response_hash from MsgFinishInference (if exists).
	// Used to verify executor completed with correct response.
	ExpectedResponseHash string `json:"expected_response_hash,omitempty"`

	// RequestTimestamp is the timestamp from MsgStartInference.
	// Used for signature verification.
	RequestTimestamp int64 `json:"request_timestamp"`

	// TransferSignature is the TA's signature from MsgStartInference.
	// Can be used to verify TA's commitment.
	TransferSignature string `json:"transfer_signature,omitempty"`
}

// VotingConfig holds configuration parameters for the voting mechanism.
type VotingConfig struct {
	// MaxNumNodes is the maximum number of nodes to select for voting until one of them votes positively
	// or all of them vote negatively.
	MaxNumNodes int `json:"max_num_nodes"`

	// VoteTimeout is the maximum time to wait for a node's vote (in ms).
	VoteTimeout int `json:"vote_timeout"`

	// MaxRetries is the maximum number of times to retry contacting a node.
	MaxRetries int `json:"max_retries"`

	// MaxNumSkips is the maximum number of nodes that may be skipped during a vote
	MaxNumSkips uint8 `json:"max_num_skips"`
}

// DefaultVotingConfig returns a VotingConfig populated with defaults.
func DefaultVotingConfig() VotingConfig {
	return VotingConfig{
		MaxNumNodes: DefaultMaxVoters,
		VoteTimeout: 0,
		MaxRetries:  1,
		MaxNumSkips: 0,
	}
}
