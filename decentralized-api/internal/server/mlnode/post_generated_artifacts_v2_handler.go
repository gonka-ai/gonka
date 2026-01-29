package mlnode

import (
	"encoding/base64"
	"errors"
	"net/http"

	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"
	"decentralized-api/poc"
	"decentralized-api/poc/artifacts"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
)

// postGeneratedArtifactsV2 handles PoC v2 artifact batch callbacks from MLNode.
// Stores artifacts locally for off-chain proofs. Store commits and weight distributions
// are submitted to chain separately by CommitWorker and the block dispatcher.
func (s *Server) postGeneratedArtifactsV2(ctx echo.Context) error {
	// V2 endpoints disabled when not in V2 mode and not in migration mode
	if s.broker != nil && !s.broker.IsV2EndpointsEnabled() {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "V2 endpoints disabled")
	}

	if s.artifactStore == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "artifact store not configured")
	}

	var body mlnodeclient.GeneratedArtifactBatchV2

	if err := ctx.Bind(&body); err != nil {
		logging.Error("ArtifactBatchV2-callback. Failed to decode request body", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	logging.Debug("ArtifactBatchV2-callback. Received", types.PoC,
		"blockHeight", body.BlockHeight,
		"publicKey", body.PublicKey,
		"nodeId", body.NodeId,
		"artifactsCount", len(body.Artifacts))

	epochState := s.broker.GetPhaseTracker().GetCurrentEpochState()
	if !poc.ShouldAcceptGeneratedArtifacts(epochState) {
		logging.Warn("ArtifactBatchV2-callback. Rejected - not in PoC generate phase", types.PoC,
			"blockHeight", body.BlockHeight)
		return echo.NewHTTPError(http.StatusServiceUnavailable, "not in PoC generate phase")
	}

	// Look up node_id string from node number
	node, found := s.broker.GetNodeByNodeNum(uint64(body.NodeId))
	if !found {
		logging.Error("ArtifactBatchV2-callback. Unknown NodeNum", types.PoC, "node_num", body.NodeId)
		return echo.NewHTTPError(http.StatusBadRequest, "unknown node_num")
	}
	nodeId := node.Id
	logging.Debug("ArtifactBatchV2-callback. Found node by node num", types.PoC,
		"nodeId", nodeId,
		"nodeNum", body.NodeId)

	// Convert artifacts from JSON format to proto format for local storage
	protoArtifacts := make([]*inference.PoCArtifactV2, 0, len(body.Artifacts))
	for _, a := range body.Artifacts {
		vectorBytes, err := base64.StdEncoding.DecodeString(a.VectorB64)
		if err != nil {
			logging.Error("ArtifactBatchV2-callback. Failed to decode artifact vector", types.PoC,
				"nonce", a.Nonce, "error", err)
			return echo.NewHTTPError(http.StatusBadRequest, "invalid base64 in artifact vector")
		}
		if len(vectorBytes) == 0 {
			logging.Error("ArtifactBatchV2-callback. Empty artifact vector", types.PoC,
				"nonce", a.Nonce)
			return echo.NewHTTPError(http.StatusBadRequest, "empty artifact vector")
		}
		protoArtifacts = append(protoArtifacts, &inference.PoCArtifactV2{
			Nonce:  int32(a.Nonce),
			Vector: vectorBytes,
		})
	}

	// Store artifacts locally for off-chain proofs
	// Store commits (MsgPoCV2StoreCommit) are submitted by CommitWorker
	// Weight distributions (MsgMLNodeWeightDistribution) are submitted at end of generation
	totalCount, nodeDistribution := s.addToLocalStorage(body.BlockHeight, nodeId, protoArtifacts)

	logging.Debug("ArtifactBatchV2-callback. Stored locally", types.PoC,
		"blockHeight", body.BlockHeight,
		"nodeId", nodeId,
		"artifactsCount", len(protoArtifacts),
		"totalInStore", totalCount,
		"nodeDistribution", nodeDistribution)

	return ctx.NoContent(http.StatusOK)
}

// postValidatedArtifactsV2 handles PoC v2 validation result callbacks from MLNode.
// Receives validation results and submits them to chain via MsgSubmitPocValidationsV2 (batch).
func (s *Server) postValidatedArtifactsV2(ctx echo.Context) error {
	// V2 endpoints disabled when not in V2 mode and not in migration mode
	if s.broker != nil && !s.broker.IsV2EndpointsEnabled() {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "V2 endpoints disabled")
	}

	var body mlnodeclient.ValidatedResultV2

	if err := ctx.Bind(&body); err != nil {
		logging.Error("ValidatedArtifactsV2-callback. Failed to decode request body", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	logging.Debug("ValidatedArtifactsV2-callback. Received", types.PoC,
		"blockHeight", body.BlockHeight,
		"publicKey", body.PublicKey,
		"nTotal", body.NTotal,
		"fraudDetected", body.FraudDetected)

	epochState := s.broker.GetPhaseTracker().GetCurrentEpochState()
	if !poc.ShouldAcceptValidatedArtifacts(epochState) {
		logging.Warn("ValidatedArtifactsV2-callback. Rejected - not in PoC validate phase", types.PoC,
			"blockHeight", body.BlockHeight)
		return echo.NewHTTPError(http.StatusServiceUnavailable, "not in PoC validate phase")
	}

	// Convert public key to bech32 address
	// PoC validation provides hex-encoded public keys
	address, err := cosmos_client.PubKeyHexToAddress(body.PublicKey)
	if err != nil {
		logging.Error("ValidatedArtifactsV2-callback. Failed to convert public key to address", types.PoC,
			"publicKey", body.PublicKey,
			"nTotal", body.NTotal,
			"fraudDetected", body.FraudDetected,
			"error", err)
		return err
	}

	// Convert fraud_detected + n_total to validated_weight
	validatedWeight := body.ToValidatedWeight()

	logging.Info("ValidatedArtifactsV2-callback. Submitting validation", types.PoC,
		"participant", address,
		"validatedWeight", validatedWeight,
		"fraudDetected", body.FraudDetected)

	// Use batch submission (even for single validation - no single-validation RPC exists)
	msg := &inference.MsgSubmitPocValidationsV2{
		PocStageStartBlockHeight: body.BlockHeight,
		Validations: []*inference.PoCValidationPayloadV2{
			{
				ParticipantAddress: address,
				ValidatedWeight:    validatedWeight,
			},
		},
	}

	if err := s.recorder.SubmitPocValidationsV2(msg); err != nil {
		logging.Error("ValidatedArtifactsV2-callback. Failed to submit MsgSubmitPocValidationsV2", types.PoC,
			"participant", address,
			"error", err)
		return err
	}

	return ctx.NoContent(http.StatusOK)
}

func (s *Server) addToLocalStorage(pocStageStartHeight int64, nodeId string, protoArtifacts []*inference.PoCArtifactV2) (uint32, map[string]uint32) {
	if s.artifactStore == nil {
		return 0, nil
	}

	store, err := s.artifactStore.GetOrCreateStore(pocStageStartHeight)
	if err != nil {
		logging.Error("Failed to get artifact store", types.PoC,
			"pocStageStartHeight", pocStageStartHeight, "error", err)
		return 0, nil
	}

	for _, a := range protoArtifacts {
		if err := store.AddWithNode(int32(a.Nonce), a.Vector, nodeId); err != nil {
			if errors.Is(err, artifacts.ErrDuplicateNonce) {
				continue
			}
			logging.Error("Failed to store artifact", types.PoC, "nonce", a.Nonce, "error", err)
		}
	}

	return store.Count(), store.GetNodeCounts()
}
