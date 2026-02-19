// Package voting provides types and services for the node voting mechanism.
package voting

import (
	"bytes"
	"context"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/apitypes"
	"decentralized-api/logging"
	"decentralized-api/payloadstorage"
	apiutils "decentralized-api/utils"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/cmd/inferenced/cmd"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

// NodePinger handles HTTP communication with nodes for payload verification.
// Used by:
// - Voters: to ping respondent and verify payload exists, return payload in vote response
// - Challengers: to request verification from pre-sampled voters
type NodePinger struct {
	httpClient         *http.Client
	cosmosClient       cosmosclient.CosmosMessageClient
	timeout            time.Duration
	inferenceIdTracker *InferenceIdTracker
}

// NodePingerConfig holds configuration for NodePinger.
type NodePingerConfig struct {
	// Timeout for HTTP requests (default: 10s)
	Timeout time.Duration
}

// DefaultNodePingerConfig returns sensible defaults.
func DefaultNodePingerConfig() NodePingerConfig {
	return NodePingerConfig{
		Timeout: 10 * time.Second,
	}
}

// NewNodePinger creates a new NodePinger instance.
func NewNodePinger(
	cosmosClient cosmosclient.CosmosMessageClient,
	inferenceIdTracker *InferenceIdTracker,
	config NodePingerConfig,
) *NodePinger {
	if config.Timeout == 0 {
		config.Timeout = DefaultNodePingerConfig().Timeout
	}

	return &NodePinger{
		httpClient:         apiutils.NewHttpClient(config.Timeout),
		cosmosClient:       cosmosClient,
		timeout:            config.Timeout,
		inferenceIdTracker: inferenceIdTracker,
	}
}

// Types for Payload Retrieval (used by voters to ping respondent)

// PingResult contains the result of pinging a node for payload.
type PingResult struct {
	// Payload contains the retrieved payload data (if successful).
	Payload *apitypes.PayloadResponse
	// PromptHash is the computed hash of the prompt payload.
	PromptHash string
	// Error contains any error that occurred.
	Error error
}

// Types for Verification Request (used by challenger to request from voters)

// VerificationRequest is sent by challenger to voters asking them to verify the respondent.
type VerificationRequest struct {
	InferenceId       string `json:"inference_id"`
	RespondentAddress string `json:"respondent_address"`
	RespondentURL     string `json:"respondent_url"`
	EpochId           uint64 `json:"epoch_id"`
	ChallengerSig     string `json:"challenger_signature"`
}

// VerificationResponse is returned by voters after verification.
type VerificationResponse struct {
	InferenceId    string   `json:"inference_id"`
	Vote           VoteType `json:"vote"`
	VoterAddress   string   `json:"voter_address"`
	VoterSignature string   `json:"voter_signature"`
	// DataFound indicates if respondent had the payload
	DataFound bool `json:"data_found"`
	// Payload contains the actual payload data retrieved from respondent (if found).
	// Returned synchronously to challenger in the same response.
	Payload *apitypes.PayloadResponse `json:"payload,omitempty"`
	// PromptHash is the hash of payload found (if any)
	PromptHash string `json:"prompt_hash,omitempty"`
	// Error message if verification failed
	ErrorMsg string `json:"error,omitempty"`
	// Timestamp when the vote was cast
	Timestamp int64 `json:"timestamp,omitempty"`
}

type InferenceIdRegisterType uint

const (
	Unregistered InferenceIdRegisterType = iota
	RegisteredByTARequest
	RegisteredByNodePinger
	RegisteredByNodePingerWithVotes
)

// Tracks which inference IDs have begun processing
type InferenceIdTracker struct {
	tracker map[string]InferenceIdRegisterType
	mutex   sync.Mutex
}

func NewInferenceIdTracker() *InferenceIdTracker {
	return &InferenceIdTracker {
		tracker: map[string]InferenceIdRegisterType{},
		mutex:   sync.Mutex{},
	}
}

// Tries to register the given register type with the given inference ID.
// If it's unregistered, doesn't do anything.
// Returns the previous register type.
func (tracker *InferenceIdTracker) TryRegisterInferenceId(
	inferenceId string,
	registerType InferenceIdRegisterType,
) InferenceIdRegisterType {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	if registerType := tracker.tracker[inferenceId]; registerType != Unregistered {
		return registerType
	}

	tracker.tracker[inferenceId] = registerType
	return Unregistered
}

// Deletes the register type for the given inference ID.
func (tracker *InferenceIdTracker) UnregisterInferenceId(inferenceId string) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	delete(tracker.tracker, inferenceId)
}

// Voter Functions: Ping Respondent and Relay to Challenger

// PingRespondentForPayload pings the respondent's payload endpoint to check if they have the payload.
// Used by voters during verification.
func (np *NodePinger) PingRespondentForPayload(
	ctx context.Context,
	respondentURL string,
	inferenceId string,
	epochId uint64,
) (*PingResult, error) {
	// Build URL with inference_id as query parameter
	baseUrl, readBodyErr := url.JoinPath(respondentURL, "v1/inference/prompt")
	if readBodyErr != nil {
		logging.Error("Failed to build retrieval URL", types.Voting, "error", readBodyErr)
		return &PingResult{Error: fmt.Errorf("failed to build URL: %w", readBodyErr)}, readBodyErr
	}

	parsedUrl, readBodyErr := url.Parse(baseUrl)
	if readBodyErr != nil {
		logging.Error("Failed to parse URL", types.Voting, "error", readBodyErr)
		return &PingResult{Error: fmt.Errorf("failed to parse URL: %w", readBodyErr)}, readBodyErr
	}

	query := parsedUrl.Query()
	query.Set("inference_id", inferenceId)
	parsedUrl.RawQuery = query.Encode()
	requestUrl := parsedUrl.String()

	// Sign the request
	timestamp := time.Now().UnixNano()
	voterAddress := np.cosmosClient.GetAccountAddress()

	signature, readBodyErr := np.signPayloadRequest(inferenceId, timestamp, voterAddress, epochId)
	if readBodyErr != nil {
		logging.Error("Failed to sign request", types.Voting, "error", readBodyErr)
		return &PingResult{Error: fmt.Errorf("failed to sign request: %w", readBodyErr)}, readBodyErr
	}

	// Create request
	req, readBodyErr := http.NewRequestWithContext(ctx, http.MethodGet, requestUrl, nil)
	if readBodyErr != nil {
		logging.Error("Failed to create retrieval request", types.Voting, "error", readBodyErr)
		return &PingResult{Error: fmt.Errorf("failed to create request: %w", readBodyErr)}, readBodyErr
	}

	// Set authentication headers
	req.Header.Set(apiutils.XRequesterAddressHeader, voterAddress)
	req.Header.Set(apiutils.XTimestampHeader, strconv.FormatInt(timestamp, 10))
	req.Header.Set(apiutils.XEpochIdHeader, strconv.FormatUint(epochId, 10))
	req.Header.Set(apiutils.AuthorizationHeader, signature)

	// Execute request
	resp, readBodyErr := np.httpClient.Do(req)
	if readBodyErr != nil {
		logging.Debug("Payload ping to respondent failed", types.Voting,
			"respondentURL", respondentURL, "inferenceId", inferenceId, "error", readBodyErr)
		return &PingResult{Error: fmt.Errorf("request failed: %w", readBodyErr)}, readBodyErr
	}
	defer resp.Body.Close()

	// Handle response codes
	body, readBodyErr := io.ReadAll(resp.Body)
	if readBodyErr != nil {
		logging.Error("Failed to read response body", types.Voting, "error", readBodyErr)
	}

	if resp.StatusCode == http.StatusNotFound {
		logging.Debug("Payload not found on respondent", types.Voting,
			"respondentURL", respondentURL, "inferenceId", inferenceId,
			"body", string(body))
		return &PingResult{Error: nil}, nil // Not found is not an error, just no data
	}

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("respondent returned status %d: %s", resp.StatusCode, string(body))
		return &PingResult{Error: err}, err
	}

	if readBodyErr != nil {
		return &PingResult{Error: readBodyErr}, readBodyErr
	}

	// Parse response
	var payloadResp apitypes.PayloadResponse
	if err := json.Unmarshal(body, &payloadResp); err != nil {
		return &PingResult{Error: fmt.Errorf("failed to decode response: %w", err)}, err
	}

	// Compute prompt hash
	promptHash, readBodyErr := payloadstorage.ComputePromptHash(payloadResp.PromptPayload)
	if readBodyErr != nil {
		logging.Warn("Failed to compute prompt hash", types.Voting,
			"inferenceId", inferenceId, "error", readBodyErr)
		promptHash = ""
	}

	logging.Debug("Successfully pinged respondent for payload", types.Voting,
		"respondentURL", respondentURL, "inferenceId", inferenceId, "promptHash", promptHash)

	return &PingResult{
		Payload:    &payloadResp,
		PromptHash: promptHash,
	}, nil
}

// Payload returned synchronously in VerificationResponse.
// - Challenger requests verification from voter
// - Voter pings respondent and returns vote + payload in same response
// - Challenger receives everything in one HTTP transaction

// VerifyRespondent is the main voter function: ping respondent and return result.
// Returns the verification result with payload (if found) that will be sent back to challenger.
func (np *NodePinger) VerifyRespondent(
	ctx context.Context,
	respondentURL string,
	inferenceId string,
	epochId uint64,
) *VerificationResponse {
	voterAddress := np.cosmosClient.GetAccountAddress()

	response := &VerificationResponse{
		InferenceId:  inferenceId,
		VoterAddress: voterAddress,
		Vote:         types.VoteType_VoteInvalid, // Default to invalid until we determine
	}

	// Step 1: Ping respondent for payload
	pingResult, err := np.PingRespondentForPayload(ctx, respondentURL, inferenceId, epochId)
	response.Timestamp = time.Now().UnixNano()
	if err != nil {
		response.ErrorMsg = err.Error()
		return response
	}

	if pingResult.Error != nil || pingResult.Payload == nil {
		// Respondent doesn't have payload - negative vote
		response.Vote = types.VoteType_VoteNegative
		response.DataFound = false
		logging.Info("Voter verification: respondent does not have payload", types.Voting,
			"inferenceId", inferenceId, "voterAddress", voterAddress)
		return response
	}

	// Respondent has payload
	response.DataFound = true
	response.PromptHash = pingResult.PromptHash
	response.Payload = pingResult.Payload // Include payload in response for challenger

	// Respondent has correct payload - positive vote
	response.Vote = types.VoteType_VotePositive
	logging.Info("Voter verification: respondent has correct payload", types.Voting,
		"inferenceId", inferenceId, "voterAddress", voterAddress)

	return response
}

// Pinging from executor to TA

func (np *NodePinger) RetrievePayloadToRequester(ctx context.Context, inferenceId string) error {
	queryClient := np.cosmosClient.NewInferenceQueryClient()
	inferenceResp, err := queryClient.Inference(ctx, &types.QueryGetInferenceRequest{Index: inferenceId})
	if err != nil {
		logging.Error("Failed to query inference", types.Voting, "inferenceId", inferenceId, "error", err)
		return err
	}

	executorAddress := inferenceResp.Inference.AssignedTo
	currentAddress := np.cosmosClient.GetAccountAddress()
	if executorAddress != currentAddress {
		// This inference ID was not meant for us; do nothing
		return nil
	}

	if !np.registerInitiatePostChat(inferenceId, inferenceResp.Inference.Status, false) {
		return nil
	}
	defer np.inferenceIdTracker.UnregisterInferenceId(inferenceId)

	transferAddress := inferenceResp.Inference.TransferredBy
	logging.Info(
		"Matched inference start event", types.Voting,
		"executorAddress", executorAddress,
		"inferenceId", inferenceId,
		"transferAddress", transferAddress,
	)

	transferURL, err := np.GetAddressUrl(ctx, transferAddress)
	if err != nil {
		logging.Error("Failed to get transfer URL", types.Voting, "error", err)
		return err
	}

	epochId := inferenceResp.Inference.EpochId
	payload, err := np.PingRespondentForPayload(ctx, transferURL, inferenceId, epochId)
	if err != nil {
		logging.Error("Failed to request payload from transfer agent", types.Voting, "epochId", epochId, "inferenceId", inferenceId, "transferURL", transferURL, "error", err)
		return err
	}

	logging.Debug("Got payload", types.Voting, "payload", payload)

	if payload.Payload == nil {
		logging.Error("Payload response is empty", types.Voting, "inferenceId", inferenceId)
		return fmt.Errorf("received empty payload for inference %s", inferenceId)
	}

	executorURL, err := np.GetAddressUrl(ctx, executorAddress)
	if err != nil {
		logging.Error("Failed to get executor URL", types.Voting, "error", err)
		return err
	}

	// Make a voting result, used only for validation.
	// This will be removed from the request before being broadcast on-chain.
	completedAt := time.Now().UnixNano()
	votes := []*inference.SignedVote{}
	signature, err := np.signVotingResult(inferenceId, votes, completedAt, executorAddress)
	if err != nil {
		return err
	}
	votingResult := &inference.VotingResult{
		InferenceId: inferenceId,
		Votes: votes,
		CompletedAt: completedAt,
		RequesterAddress: executorAddress,
		RequesterSignature: signature,
	}
	err = np.PostChat(executorURL, payload.Payload.PromptPayload, votingResult)
	if err != nil {
		logging.Error("Failed to post chat request to executor", types.Voting, "inferenceId", inferenceId, "executorURL", executorURL, "executorAddress", executorAddress, "error", err)
		return err
	}

	return nil
}

func (np *NodePinger) PostChat(
	executorURL string,
	payloadBytes []byte,
	votingResult *inference.VotingResult,
) error {
	var chatRequest apitypes.ChatRequest
	err := json.Unmarshal(payloadBytes, &chatRequest)
	if err != nil {
		logging.Error("Failed to unmarshal chat request", types.Voting, "error", err)
		return err
	}

	// TODO: It's a bit silly to make a request to ourselves.
	chatURL, err := url.JoinPath(executorURL, "v1/chat/completions")
	if err != nil {
		logging.Error("Failed to build completions URL", types.Voting, "error", err)
		return err
	}

	req, err := http.NewRequest("POST", chatURL, bytes.NewBuffer(chatRequest.Body))
	if err != nil {
		logging.Error("Failed to create POST request to completions URL", types.Voting, "chatURL", chatURL, "error", err)
		return err
	}

	req.Header.Set(apiutils.XInferenceIdHeader, chatRequest.InferenceId)
	req.Header.Set(apiutils.XSeedHeader, chatRequest.Seed)
	req.Header.Set(apiutils.AuthorizationHeader, chatRequest.AuthKey)
	req.Header.Set(apiutils.XTimestampHeader, strconv.FormatInt(chatRequest.Timestamp, 10))
	req.Header.Set(apiutils.XTransferAddressHeader, chatRequest.TransferAddress)
	req.Header.Set(apiutils.XRequesterAddressHeader, chatRequest.RequesterAddress)
	req.Header.Set(apiutils.XTASignatureHeader, chatRequest.TransferSignature)
	req.Header.Set(apiutils.XPromptHashHeader, chatRequest.PromptHash)
	req.Header.Set(apiutils.ContentTypeHeader, chatRequest.ContentType)
	if votingResult != nil {
		// Inject voting result for executor request
		votingResultBytes, err := json.Marshal(votingResult)
		if err != nil {
			logging.Error("Failed to marshal voting result", types.Voting, "error", err)
			return err
		}
		req.Header.Set(apiutils.XVotingResult, string(votingResultBytes))
	}

	resp, err := np.httpClient.Do(req)
	if err != nil {
		logging.Error("Failed to POST to completions URL", types.Voting, "chatURL", chatURL, "error", err)
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		if err != nil {
			logging.Error("Chat request returned non-200 status code, failed to read response body", types.Voting, "statusCode", resp.StatusCode, "error", err)
			return fmt.Errorf("Chat request returned status code %d, failed to read response body: %w", resp.StatusCode, err)
		}

		logging.Error("Chat request returned non-200 status code", types.Voting, "statusCode", resp.StatusCode, "body", body)
		return fmt.Errorf("Chat request returned status code %d: %s", resp.StatusCode, string(body))
	}
	if err != nil {
		logging.Error("Failed to read response body", types.Voting, "error", err)
		return err
	}

	// TODO: proxy response?
	return nil
}

func (np *NodePinger) GetAddressUrl(ctx context.Context, address string) (string, error) {
	queryClient := np.cosmosClient.NewInferenceQueryClient()
	participantResp, err := queryClient.Participant(ctx, &types.QueryGetParticipantRequest{
		Index: address,
	})
	if err != nil {
		logging.Error("Failed to query address", types.Voting, "error", err)
		return "", err
	}
	return participantResp.Participant.InferenceUrl, nil
}

func (np *NodePinger) registerInitiatePostChat(inferenceId string, status types.InferenceStatus, isVoting bool) bool {
	if status != types.InferenceStatus_STARTED {
		logging.Debug("Inference status is not started; skipping", types.Voting, "inferenceId", inferenceId, "status", status)
		return false
	}

	var registerType InferenceIdRegisterType
	if isVoting {
		registerType = RegisteredByNodePingerWithVotes
	} else {
		registerType = RegisteredByNodePinger
	}

	if oldRegisterType := np.inferenceIdTracker.TryRegisterInferenceId(inferenceId, registerType); oldRegisterType != Unregistered {
		logging.Debug(
			"Inference has already begun processing; skipping", types.Voting,
			"inferenceId", inferenceId,
			"registerType", registerType,
			"oldRegisterType", oldRegisterType,
		)
		return false
	}

	return true
}

// VoterFallback is called when the executor's direct payload retrieval from the TA fails.
// It samples voters from active participants and requests verification.
// If a voter finds the payload on the TA, the voter returns it and the executor uses it.
func (np *NodePinger) VoterFallback(ctx context.Context, inferenceId string) error {
	queryClient := np.cosmosClient.NewInferenceQueryClient()
	inferenceResp, err := queryClient.Inference(ctx, &types.QueryGetInferenceRequest{Index: inferenceId})
	if err != nil {
		logging.Error("VoterFallback: failed to query inference", types.Voting,
			"inferenceId", inferenceId, "error", err)
		return err
	}

	executorAddress := inferenceResp.Inference.AssignedTo
	currentAddress := np.cosmosClient.GetAccountAddress()
	if executorAddress != currentAddress {
		// Not our inference
		return nil
	}

	if !np.registerInitiatePostChat(inferenceId, inferenceResp.Inference.Status, true) {
		return nil
	}
	defer np.inferenceIdTracker.UnregisterInferenceId(inferenceId)

	transferAddress := inferenceResp.Inference.TransferredBy
	epochId := inferenceResp.Inference.EpochId

	logging.Info("VoterFallback: initiating voter verification", types.Voting,
		"inferenceId", inferenceId,
		"executorAddress", executorAddress,
		"transferAddress", transferAddress,
		"epochId", epochId)

	// Get TA URL for the verification request
	transferURL, err := np.GetAddressUrl(ctx, transferAddress)
	if err != nil {
		logging.Error("VoterFallback: failed to get TA URL", types.Voting, "error", err)
		return err
	}

	// Sample voters using replayable random (exclude TA and executor).
	sampledVoters, err := SampleVotersForInference(
		ctx, np.cosmosClient, &inferenceResp.Inference,
		DefaultMaxVoters, transferAddress, executorAddress,
	)
	if err != nil {
		logging.Error("VoterFallback: failed to sample voters", types.Voting, "error", err)
		return err
	}

	voterURLs := make([]string, len(sampledVoters))
	for i, v := range sampledVoters {
		voterURLs[i] = v.InferenceURL
	}

	if len(voterURLs) == 0 {
		logging.Warn("VoterFallback: no voters available", types.Voting,
			"inferenceId", inferenceId)
		return fmt.Errorf("no voters available for inference %s", inferenceId)
	}

	// Create verification request.
	// Note: PromptHash is left empty intentionally. The on-chain hash is computed from the
	// canonicalized request body, but prompt storage stores the full ChatRequest (with auth keys,
	// timestamps, etc.). Comparing hashes here would always mismatch. The voter's job is to
	// check payload availability, not hash correctness.
	verificationRequest := &VerificationRequest{
		InferenceId:       inferenceId,
		RespondentAddress: transferAddress,
		RespondentURL:     transferURL,
		EpochId:           epochId,
	}

	votingCfg := DefaultVotingConfig()
	// Override defaults with NodePinger-specific settings.
	votingCfg.VoteTimeout = int(np.timeout.Milliseconds())
	votingCfg.MaxRetries = 2

	// Request verification from voters
	result, err := np.RequestVerificationFromVoters(ctx, voterURLs, verificationRequest, votingCfg)
	if err != nil {
		logging.Error("VoterFallback: verification request failed", types.Voting,
			"inferenceId", inferenceId, "error", err)
		return err
	}

	votingResult, err := np.createVotingResult(result)
	if err != nil {
		logging.Error("VoterFallback: Failed to sign voting result", types.Voting, "inferenceId", inferenceId, "error", err)
		return err
	}

	// Check result: if we got a positive vote with payload, use it
	// Otherwise, we post `MsgFinishInferenceWithMissingPayload` indicating that
	// the vote was negative.
	if result.FirstPositive != nil && result.FirstPositive.Response != nil &&
		result.FirstPositive.Response.Payload != nil {
		logging.Info("VoterFallback: received payload from voter", types.Voting,
			"inferenceId", inferenceId,
			"voterURL", result.FirstPositive.VoterURL,
			"vote", result.FirstPositive.Response.Vote)

		// Forward the payload to ourselves (the executor) via PostChat
		executorURL, err := np.GetAddressUrl(ctx, executorAddress)
		if err != nil {
			logging.Error("VoterFallback: failed to get executor URL", types.Voting, "error", err)
			return err
		}

		err = np.PostChat(executorURL, result.FirstPositive.Response.Payload.PromptPayload, votingResult)
		if err != nil {
			logging.Error("VoterFallback: failed to post chat to executor", types.Voting,
				"inferenceId", inferenceId, "error", err)
			return err
		}

		logging.Info("VoterFallback: successfully forwarded payload to executor", types.Voting,
			"inferenceId", inferenceId)
		return nil
	} else {
		executorSignature, err := np.signMsgFinishInference(
			inferenceResp.Inference.PromptHash,
			inferenceResp.Inference.RequestTimestamp,
			transferAddress,
			executorAddress,
			0,
		)
		if err != nil {
			logging.Error(
				"VoterFallback: failed to sign MsgFinishInference", types.Voting,
				"inferenceId", inferenceId,
				"error", err,
			)
			return err
		}

		message := &inference.MsgFinishInference {
			Creator:              executorAddress,
			InferenceId:          inferenceId,
			ResponseHash:         inferenceResp.Inference.ResponseHash,
			PromptTokenCount:     inferenceResp.Inference.PromptTokenCount,
			CompletionTokenCount: inferenceResp.Inference.CompletionTokenCount,
			ExecutedBy:           executorAddress,
			TransferredBy:        transferAddress,
			TransferSignature:    inferenceResp.Inference.TransferSignature,
			ExecutorSignature:    executorSignature,
			RequestTimestamp:     inferenceResp.Inference.RequestTimestamp,
			RequestedBy:          inferenceResp.Inference.RequestedBy,
			Model:                inferenceResp.Inference.Model,
			PromptHash:           inferenceResp.Inference.PromptHash,
			OriginalPromptHash:   inferenceResp.Inference.OriginalPromptHash,
		}
		messageWithMissingPayload := &inference.MsgFinishInferenceWithMissingPayload {
			MsgFinishInference: message,
			VotingResult: votingResult,
		}

		logging.Warn(
			"VoterFallback: posting MsgFinishInferenceWithMissingPayload with negative voting outcome", types.Voting,
			"inferenceId", inferenceId,
		)
		if err = np.cosmosClient.FinishInferenceWithMissingPayload(messageWithMissingPayload); err != nil {
			logging.Error(
				"VoterFallback: failed to MsgFinishInferenceWithMissingPayload with negative voting outcome", types.Voting,
				"inferenceId", inferenceId,
				"error", err,
			)
			return err
		}
	}

	// All voters returned negative — payload doesn't exist
	logging.Warn("VoterFallback: all voters returned negative votes", types.Voting,
		"inferenceId", inferenceId,
		"totalVoters", len(result.VoterResults),
		"negativeVotes", result.NegativeVotes)
	return fmt.Errorf("voter fallback failed: all %d voters returned negative for inference %s",
		result.NegativeVotes, inferenceId)
}

func (np *NodePinger) createVotingResult(result *ChallengerVotingResult) (*inference.VotingResult, error) {
	votes := []*inference.SignedVote{}
	for _, vote := range result.VoterResults {
		if vote.Error != nil {
			logging.Warn("Found error in vote", types.Voting, "error", vote.Error)
			continue
		}

		votes = append(
			votes,
			&inference.SignedVote {
				InferenceId: vote.Response.InferenceId,
				VoterAddress: vote.Response.VoterAddress,
				VoteType: inference.VoteType(vote.Response.Vote),
				RespondentDataHash: vote.Response.PromptHash, // TODO: use response hash instead?
				Timestamp: vote.Response.Timestamp,
				VoterSignature: vote.Response.VoterSignature,
			},
		)
	}

	completedAt := time.Now().UnixNano()
	requesterAddress := np.cosmosClient.GetAccountAddress()
	signature, err := np.signVotingResult(result.InferenceId, votes, completedAt, requesterAddress)
	if err != nil {
		return nil, err
	}

	votingResult := &inference.VotingResult {
		InferenceId: result.InferenceId,
		Votes: votes,
		CompletedAt: completedAt,
		RequesterAddress: requesterAddress,
		RequesterSignature: signature,
	}

	return votingResult, nil
}

// Challenger Functions: Request Verification from Voters

// VoterVerificationResult contains a single voter's verification outcome.
type VoterVerificationResult struct {
	VoterURL  string
	Response  *VerificationResponse
	Error     error
	Reachable bool
}

// ChallengerVotingResult contains the aggregated result of requesting verification from voters.
// Used by challenger to track voting progress.
type ChallengerVotingResult struct {
	InferenceId string
	// VoterResults contains results from all voters that were contacted
	VoterResults []VoterVerificationResult
	// NegativeVotes is the count of negative votes
	NegativeVotes int32
	// FirstPositive is the first positive voter result (if any)
	FirstPositive *VoterVerificationResult
	// StoppedEarly indicates if we stopped after finding a positive vote
	StoppedEarly bool
}

// RequestVerificationFromVoters contacts pre-sampled voters to verify the respondent.
// Voters are pinged in parallel with per-voter timeouts and retries.
// The process stops early once the first positive vote is received.
// Used by challenger to supervise the voting process.
func (np *NodePinger) RequestVerificationFromVoters(
	ctx context.Context,
	voterURLs []string,
	request *VerificationRequest,
	cfg VotingConfig,
) (*ChallengerVotingResult, error) {
	result := &ChallengerVotingResult{
		InferenceId:  request.InferenceId,
		VoterResults: make([]VoterVerificationResult, 0, len(voterURLs)),
	}

	if len(voterURLs) == 0 {
		logging.Warn("No voters provided for verification", types.Voting,
			"inferenceId", request.InferenceId)
		return result, nil
	}

	// Apply sane defaults from config.
	// By default, we cap the number of voters to DefaultMaxVoters (or fewer if not enough URLs).
	if cfg.MaxNumNodes <= 0 {
		if len(voterURLs) < DefaultMaxVoters {
			cfg.MaxNumNodes = len(voterURLs)
		} else {
			cfg.MaxNumNodes = DefaultMaxVoters
		}
	} else if cfg.MaxNumNodes > len(voterURLs) {
		cfg.MaxNumNodes = len(voterURLs)
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 1
	}
	if cfg.VoteTimeout <= 0 {
		// Fall back to NodePinger's HTTP client timeout if not specified.
		cfg.VoteTimeout = int(np.timeout.Milliseconds())
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type voterJobResult struct {
		voterURL string
		result   VoterVerificationResult
	}

	resultsCh := make(chan voterJobResult, cfg.MaxNumNodes)

	var wg sync.WaitGroup
	maxVoters := cfg.MaxNumNodes

	for i := 0; i < maxVoters; i++ {
		voterURL := voterURLs[i]

		wg.Add(1)
		go func(voterURL string) {
			defer wg.Done()

			var lastResult VoterVerificationResult

			for attempt := 0; attempt < cfg.MaxRetries; attempt++ {
				select {
				case <-ctx.Done():
					return
				default:
				}

				logging.Debug("Requesting verification from voter", types.Voting,
					"inferenceId", request.InferenceId, "voterURL", voterURL,
					"attempt", attempt+1, "maxAttempts", cfg.MaxRetries)

				// Apply per-voter timeout on top of the HTTP client's timeout.
				callCtx, cancelCall := context.WithTimeout(ctx, time.Duration(cfg.VoteTimeout)*time.Millisecond)
				res := np.requestVerificationFromSingleVoter(callCtx, voterURL, request)
				cancelCall()

				lastResult = res

				// If the voter was reachable and responded, no need to retry.
				if res.Error == nil || !res.Reachable {
					break
				}
			}

			select {
			case <-ctx.Done():
				return
			case resultsCh <- voterJobResult{voterURL: voterURL, result: lastResult}:
			}
		}(voterURL)
	}

	// Close results channel once all goroutines complete.
	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	for jobRes := range resultsCh {
		voterResult := jobRes.result
		result.VoterResults = append(result.VoterResults, voterResult)

		// If we already stopped early due to a positive vote, just record results without
		// updating the aggregated outcome further.
		if result.StoppedEarly && result.FirstPositive != nil {
			continue
		}

		if voterResult.Error != nil || !voterResult.Reachable {
			result.NegativeVotes++
			continue
		}

		if voterResult.Response != nil {
			switch voterResult.Response.Vote {
			case types.VoteType_VotePositive:
				// Capture the first positive vote and stop further work.
				resultCopy := voterResult
				result.FirstPositive = &resultCopy
				result.StoppedEarly = true

				logging.Info("Found positive vote, stopping verification", types.Voting,
					"inferenceId", request.InferenceId, "voterURL", jobRes.voterURL,
					"contacted", len(result.VoterResults))

				// Cancel the shared context so in-flight requests can be aborted.
				cancel()

			case types.VoteType_VoteNegative:
				result.NegativeVotes++

			default:
				// Invalid or unknown vote - treat as negative for aggregation.
				result.NegativeVotes++
			}
		}
	}

	if result.StoppedEarly && result.FirstPositive != nil {
		return result, nil
	}

	// If context was cancelled externally and we didn't stop because of a positive vote,
	// surface the cancellation error.
	if err := ctx.Err(); err != nil && err != context.Canceled {
		logging.Warn("Verification request cancelled or timed out", types.Voting,
			"inferenceId", request.InferenceId, "contacted", len(result.VoterResults), "error", err)
		return result, err
	}

	logging.Info("All voters completed without positive vote", types.Voting,
		"inferenceId", request.InferenceId, "totalVoters", len(result.VoterResults))

	return result, nil
}

// requestVerificationFromSingleVoter sends a verification request to one voter.
func (np *NodePinger) requestVerificationFromSingleVoter(
	ctx context.Context,
	voterURL string,
	request *VerificationRequest,
) VoterVerificationResult {
	result := VoterVerificationResult{
		VoterURL:  voterURL,
		Reachable: false,
	}

	// Build URL for voter's verify endpoint
	// TODO! Add the verify endpoint to the voter's URL.
	verifyUrl, err := url.JoinPath(voterURL, "v1/voting/verify")
	if err != nil {
		result.Error = fmt.Errorf("failed to build verify URL: %w", err)
		return result
	}

	// Marshal request
	requestBytes, err := json.Marshal(request)
	if err != nil {
		result.Error = fmt.Errorf("failed to marshal request: %w", err)
		return result
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, verifyUrl, bytes.NewReader(requestBytes))
	if err != nil {
		result.Error = fmt.Errorf("failed to create request: %w", err)
		return result
	}

	req.Header.Set("Content-Type", "application/json")

	// Sign the request
	timestamp := time.Now().UnixNano()
	challengerAddress := np.cosmosClient.GetAccountAddress()

	signature, err := np.signVerificationRequest(request.InferenceId, timestamp, challengerAddress)
	if err != nil {
		result.Error = fmt.Errorf("failed to sign request: %w", err)
		return result
	}

	req.Header.Set(apiutils.XValidatorAddressHeader, challengerAddress)
	req.Header.Set(apiutils.XTimestampHeader, strconv.FormatInt(timestamp, 10))
	req.Header.Set(apiutils.AuthorizationHeader, signature)

	// Execute request
	resp, err := np.httpClient.Do(req)
	if err != nil {
		result.Error = fmt.Errorf("request failed: %w", err)
		return result
	}
	defer resp.Body.Close()

	result.Reachable = true

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		result.Error = fmt.Errorf("voter returned status %d: %s", resp.StatusCode, string(body))
		return result
	}

	// Parse response
	var verifyResp VerificationResponse
	if err := json.NewDecoder(resp.Body).Decode(&verifyResp); err != nil {
		result.Error = fmt.Errorf("failed to decode response: %w", err)
		return result
	}

	result.Response = &verifyResp
	return result
}

// Signature Helpers

// signPayloadRequest signs a payload retrieval request.
func (np *NodePinger) signPayloadRequest(
	inferenceId string,
	timestamp int64,
	voterAddress string,
	epochId uint64,
) (string, error) {
	components := calculations.SignatureComponents{
		Payload:         inferenceId,
		EpochId:         epochId,
		Timestamp:       timestamp,
		TransferAddress: voterAddress,
		ExecutorAddress: "",
	}

	return np.sign(components, calculations.Developer)
}

// signVerificationRequest signs a verification request from challenger to voter.
func (np *NodePinger) signVerificationRequest(
	inferenceId string,
	timestamp int64,
	challengerAddress string,
) (string, error) {
	components := calculations.SignatureComponents{
		Payload:         inferenceId,
		EpochId:         0,
		Timestamp:       timestamp,
		TransferAddress: challengerAddress,
		ExecutorAddress: "",
	}

	return np.sign(components, calculations.Developer)
}

// signVerificationRequest signs a verification request from challenger to voter.
func (np *NodePinger) signMsgFinishInference(
	promptHash string,
	timestamp int64,
	transferAddress string,
	executorAddress string,
	epochId uint64,
) (string, error) {
	components := calculations.SignatureComponents{
		Payload:         promptHash,
		EpochId:         epochId,
		Timestamp:       timestamp,
		TransferAddress: transferAddress,
		ExecutorAddress: executorAddress,
	}

	return np.sign(components, calculations.ExecutorAgent)
}

func (np *NodePinger) signVotingResult(
	inferenceId string,
	votes []*inference.SignedVote,
	completedAt int64,
	requesterAddress string,
) (string, error) {
	resultBytes := votingResultBytesToSign(inferenceId, votes)
	components := calculations.SignatureComponents{
		Payload:         string(resultBytes),
		EpochId:         0,
		Timestamp:       completedAt,
		TransferAddress: requesterAddress,
		ExecutorAddress: "",
	}

	return np.sign(components, calculations.Developer)
}

// sign is a helper to sign with the cosmos client's keyring.
func (np *NodePinger) sign(
	components calculations.SignatureComponents,
	signatureType calculations.SignatureType,
) (string, error) {
	signerAddressStr := np.cosmosClient.GetSignerAddress()
	signerAddress, err := sdk.AccAddressFromBech32(signerAddressStr)
	if err != nil {
		return "", fmt.Errorf("invalid signer address: %w", err)
	}

	accountSigner := &cmd.AccountSigner{
		Addr:    signerAddress,
		Keyring: np.cosmosClient.GetKeyring(),
	}

	return calculations.Sign(accountSigner, components, signatureType)
}
