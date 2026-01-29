package mlnode

import (
	"net/http"

	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
)

// postGeneratedBatchesV1 handles V1 PoC artifact batch callbacks from MLNode.
// Submits MsgSubmitPocBatch directly to chain (on-chain storage).
func (s *Server) postGeneratedBatchesV1(ctx echo.Context) error {
	// V2 mode: V1 endpoints are disabled
	if s.broker != nil && s.broker.IsPoCv2Enabled() {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "V1 endpoints disabled when poc_v2_enabled=true")
	}

	var body mlnodeclient.ProofBatchV1

	if err := ctx.Bind(&body); err != nil {
		logging.Error("ProofBatchV1-callback. Failed to decode request body", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	logging.Debug("ProofBatchV1-callback. Received", types.PoC,
		"blockHeight", body.BlockHeight,
		"publicKey", body.PublicKey,
		"nodeNum", body.NodeNum,
		"noncesCount", len(body.Nonces))

	var nodeId string
	node, found := s.broker.GetNodeByNodeNum(body.NodeNum)
	if found {
		nodeId = node.Id
		logging.Debug("ProofBatchV1-callback. Found node by node num", types.PoC,
			"nodeId", nodeId,
			"nodeNum", body.NodeNum)
	} else {
		logging.Warn("ProofBatchV1-callback. Unknown NodeNum. Sending MsgSubmitPocBatch with empty nodeId",
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
		logging.Error("ProofBatchV1-callback. Failed to submit MsgSubmitPocBatch", types.PoC, "error", err)
		return err
	}

	return ctx.NoContent(http.StatusOK)
}

// postValidatedBatchesV1 handles V1 PoC validation result callbacks from MLNode.
// Submits MsgSubmitPocValidation to chain.
func (s *Server) postValidatedBatchesV1(ctx echo.Context) error {
	// V2 mode: V1 endpoints are disabled
	if s.broker != nil && s.broker.IsPoCv2Enabled() {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "V1 endpoints disabled when poc_v2_enabled=true")
	}

	var body mlnodeclient.ValidatedBatchV1

	if err := ctx.Bind(&body); err != nil {
		logging.Error("ValidatedBatchV1-callback. Failed to decode request body", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	logging.Debug("ValidatedBatchV1-callback. Received", types.PoC,
		"blockHeight", body.BlockHeight,
		"publicKey", body.PublicKey,
		"nInvalid", body.NInvalid,
		"fraudDetected", body.FraudDetected)

	address, err := cosmos_client.PubKeyToAddress(body.PublicKey)
	if err != nil {
		logging.Error("ValidatedBatchV1-callback. Failed to convert public key to address", types.PoC,
			"publicKey", body.PublicKey,
			"nInvalid", body.NInvalid,
			"probabilityHonest", body.ProbabilityHonest,
			"fraudDetected", body.FraudDetected,
			"error", err)
		return err
	}

	logging.Info("ValidatedBatchV1-callback. Submitting validation", types.PoC,
		"participant", address,
		"nInvalid", body.NInvalid,
		"probabilityHonest", body.ProbabilityHonest,
		"fraudDetected", body.FraudDetected)

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

	// Empty arrays to avoid large chain transactions (only FraudDetected is used for decision)
	emptyValidationArrays(msg)

	if err := s.recorder.SubmitPoCValidation(msg); err != nil {
		logging.Error("ValidatedBatchV1-callback. Failed to submit MsgSubmitPocValidation", types.PoC,
			"participant", address,
			"error", err)
		return err
	}

	return ctx.NoContent(http.StatusOK)
}

// emptyValidationArrays clears large arrays from validation message to reduce tx size.
// The chain only uses FraudDetected boolean for weight decisions.
func emptyValidationArrays(msg *inference.MsgSubmitPocValidation) {
	msg.Dist = make([]float64, 0)
	msg.ReceivedDist = make([]float64, 0)
	msg.Nonces = make([]int64, 0)
}
