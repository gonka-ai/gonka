package mlnode

import (
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"net/http"

	"decentralized-api/mlnodeclient"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
)

func (s *Server) postGeneratedBatches(ctx echo.Context) error {
	var body mlnodeclient.ProofBatch

	if err := ctx.Bind(&body); err != nil {
		logging.Error("ProofBatch-callback. Failed to decode request body of type ProofBatch", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	logging.Debug("ProofBatch-callback. Received", types.PoC, "body", body)

	var nodeId string
	node, found := s.broker.GetNodeByNodeNum(body.NodeNum)
	if found {
		nodeId = node.Id
		logging.Info("ProofBatch-callback. Found node by node num", types.PoC,
			"nodeId", nodeId,
			"nodeNum", body.NodeNum)
	} else {
		logging.Warn("ProofBatch-callback. Unknown NodeNum. Sending MsgSubmitPocBatch with empty nodeId",
			types.PoC, "node_num", body.NodeNum)
	}

	msg := &inference.MsgSubmitPocBatch{
		PocStageStartBlockHeight: body.BlockHeight,
		Nonces:                   body.Nonces,
		Dist:                     body.Dist,
		BatchId:                  uuid.New().String(),
		NodeId:                   nodeId,
	}

	if err := s.recorder.SubmitPocBatch(msg); err != nil {
		logging.Error("ProofBatch-callback. Failed to submit MsgSubmitPocBatch", types.PoC, "error", err)
		return err
	}

	return ctx.NoContent(http.StatusOK)
}

func (s *Server) postValidatedBatches(ctx echo.Context) error {
	var body mlnodeclient.ValidatedBatch

	if err := ctx.Bind(&body); err != nil {
		logging.Error("ValidateReceivedBatches-callback. Failed to decode request body of type ValidatedBatch", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	logging.Debug("ValidateReceivedBatches-callback. ValidatedProofBatch received", types.PoC, "body", body)

	address, err := cosmos_client.PubKeyToAddress(body.PublicKey)
	if err != nil {
		logging.Error("ValidateReceivedBatches-callback. Failed to convert public key to address", types.PoC,
			"publicKey", body.PublicKey,
			"NInvalid", body.NInvalid,
			"ProbabilityHonest", body.ProbabilityHonest,
			"FraudDetected", body.FraudDetected,
			"error", err)
		return err
	}

	logging.Info("ValidateReceivedBatches-callback. ValidatedProofBatch received", types.PoC,
		"participant", address,
		"NInvalid", body.NInvalid,
		"ProbabilityHonest", body.ProbabilityHonest,
		"FraudDetected", body.FraudDetected)

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
		logging.Error("ValidateReceivedBatches-callback. Failed to submit MsgSubmitValidatedPocBatch", types.PoC,
			"participant", address,
			"error", err)
		return err
	}

	return ctx.NoContent(http.StatusOK)
}

func emptyArrays(msg *inference.MsgSubmitPocValidation) {
	msg.Dist = make([]float64, 0)
	msg.ReceivedDist = make([]float64, 0)
	msg.Nonces = make([]int64, 0)
}
