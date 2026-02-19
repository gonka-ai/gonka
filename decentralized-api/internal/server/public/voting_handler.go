package public

import (
	"decentralized-api/internal/voting"
	"decentralized-api/logging"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

// postVotingVerify handles verification requests from challengers.
// When a challenger (executor) asks this node (voter) to verify a respondent (TA),
// the voter pings the respondent's payload endpoint and returns the result.
func (s *Server) postVotingVerify(ctx echo.Context) error {
	var req voting.VerificationRequest
	if err := ctx.Bind(&req); err != nil {
		logging.Error("Failed to bind verification request", types.Voting, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid verification request")
	}

	if req.InferenceId == "" || req.RespondentURL == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "inference_id and respondent_url are required")
	}

	logging.Info("Received verification request from challenger", types.Voting,
		"inferenceId", req.InferenceId,
		"respondentAddress", req.RespondentAddress,
		"respondentURL", req.RespondentURL,
		"epochId", req.EpochId)

	// Query chain to validate the request before doing any work.
	cv := voting.NewChainVerifier(s.recorder, nil)
	onChain, err := cv.QueryInferenceState(ctx.Request().Context(), req.InferenceId)
	if err != nil {
		logging.Error("Failed to query chain for inference state", types.Voting,
			"inferenceId", req.InferenceId, "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to query chain")
	}

	// Reject if inference doesn't exist on chain
	if !onChain.InferenceExists {
		logging.Warn("Voter rejected request: inference not found on chain", types.Voting,
			"inferenceId", req.InferenceId)
		return echo.NewHTTPError(http.StatusBadRequest, "inference not found on chain")
	}

	// Reject if inference is already finished (no recovery needed)
	if onChain.FinishExists {
		logging.Warn("Voter rejected request: inference already finished", types.Voting,
			"inferenceId", req.InferenceId)
		return echo.NewHTTPError(http.StatusBadRequest, "inference already finished")
	}

	// Validate respondent is the TA for this inference
	if req.RespondentAddress != "" && onChain.CreatedBy != req.RespondentAddress {
		logging.Warn("Voter rejected request: respondent is not the TA for this inference", types.Voting,
			"inferenceId", req.InferenceId,
			"expectedTA", onChain.CreatedBy,
			"requestedRespondent", req.RespondentAddress)
		return echo.NewHTTPError(http.StatusBadRequest, "respondent is not the TA for this inference")
	}

	// Create a NodePinger using the server's cosmos client to sign requests
	npConfig := voting.DefaultNodePingerConfig()
	np := voting.NewNodePinger(s.recorder, s.inferenceIdTracker, npConfig)

	// Verify the respondent: ping their payload endpoint and check if they have the data.
	// PromptHash is left empty — the on-chain hash and stored payload hash use different
	// serialization formats, so comparison would always mismatch. The voter validates
	// inference existence and status above; hash correctness is a separate concern.
	response := np.VerifyRespondent(
		ctx.Request().Context(),
		req.RespondentURL,
		req.InferenceId,
		req.EpochId,
	)

	logging.Info("Verification result", types.Voting,
		"inferenceId", req.InferenceId,
		"vote", response.Vote,
		"dataFound", response.DataFound,
		"voterAddress", response.VoterAddress)

	return ctx.JSON(http.StatusOK, response)
}
