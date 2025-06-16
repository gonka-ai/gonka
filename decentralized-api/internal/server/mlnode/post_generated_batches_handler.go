package mlnode

import (
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/internal/poc"
	"decentralized-api/logging"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
)

func (s *Server) postGeneratedBatches(ctx echo.Context) error {
	var body poc.ProofBatch

	if err := ctx.Bind(&body); err != nil {
		logging.Error("Failed to decode request body of type ProofBatch", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	logging.Info("ProofBatch received", types.PoC, "body", body)

	msg := &inference.MsgSubmitPocBatch{
		PocStageStartBlockHeight: body.BlockHeight,
		Nonces:                   body.Nonces,
		Dist:                     body.Dist,
		BatchId:                  uuid.New().String(),
	}

	if err := s.recorder.SubmitPocBatch(msg); err != nil {
		logging.Error("Failed to submit MsgSubmitPocBatch", types.PoC, "error", err)
		return err
	}

	return ctx.NoContent(http.StatusOK)
}

func (s *Server) postValidatedBatches(ctx echo.Context) error {
	var body poc.ValidatedBatch

	if err := ctx.Bind(&body); err != nil {
		logging.Error("Failed to decode request body of type ValidatedBatch", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	logging.Info("ValidatedProofBatch received", types.PoC, "NInvalid", body.NInvalid, "ProbabilityHonest", body.ProbabilityHonest, "FraudDetected", body.FraudDetected)
	logging.Debug("ValidatedProofBatch received", types.PoC, "body", body)

	address, err := cosmos_client.PubKeyToAddress(body.PublicKey)
	if err != nil {
		logging.Error("Failed to convert public key to address", types.PoC, "error", err)
		return err
	}

	msg := &inference.MsgSubmitPocValidation{
		ParticipantAddress:       address,
		PocStageStartBlockHeight: body.BlockHeight,
		Nonces:                   body.Nonces,
		Dist:                     body.Dist,
		ReceivedDist:             body.ReceivedDist,
		RTarget:                  body.RTarget,
		FraudThreshold:           body.FraudThreshold,
		NInvalid:                 body.NInvalid,
		ProbabilityHonest:        body.ProbabilityHonest,
		FraudDetected:            body.FraudDetected,
	}

	// FIXME: We empty all arrays to avoid too large chain transactions
	//  We can allow that, because we only use FraudDetected boolean
	//  when making a decision about participant's PoC submissions
	//  Will be fixed in future versions
	emptyArrays(msg)

	if err := s.recorder.SubmitPoCValidation(msg); err != nil {
		logging.Error("Failed to submit MsgSubmitValidatedPocBatch", types.PoC, "error", err)
		return err
	}

	return ctx.NoContent(http.StatusOK)
}

func emptyArrays(msg *inference.MsgSubmitPocValidation) {
	msg.Dist = make([]float64, 0)
	msg.ReceivedDist = make([]float64, 0)
	msg.Nonces = make([]int64, 0)
}
