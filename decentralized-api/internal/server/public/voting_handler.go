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

	// Create a NodePinger using the server's cosmos client to sign requests
	npConfig := voting.DefaultNodePingerConfig()
	np := voting.NewNodePinger(s.recorder, npConfig)

	// Verify the respondent: ping their payload endpoint and check if they have the data
	response := np.VerifyRespondent(
		ctx.Request().Context(),
		req.RespondentURL,
		req.InferenceId,
		req.EpochId,
		req.PromptHash,
	)

	logging.Info("Verification result", types.Voting,
		"inferenceId", req.InferenceId,
		"vote", response.Vote,
		"dataFound", response.DataFound,
		"voterAddress", response.VoterAddress)

	return ctx.JSON(http.StatusOK, response)
}
