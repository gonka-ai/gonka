package mlnode

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"

	"common/logging"
	"common/utils"
	"decentralized-api/mlnodeclient"
	"decentralized-api/poc"
	"decentralized-api/poc/artifacts"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

func decodeCallbackModelID(encoded string) (string, error) {
	if encoded == "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "model_id required")
	}
	modelID, err := url.PathUnescape(encoded)
	if err != nil || modelID == "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "invalid model_id")
	}
	return modelID, nil
}

// postGeneratedArtifactsV2 handles PoC v2 artifact batch callbacks from MLNode.
// Stores artifacts locally for off-chain proofs. Store commits and weight distributions
// are submitted to chain separately by CommitWorker and the block dispatcher.
func (s *Server) postGeneratedArtifactsV2(ctx echo.Context) error {
	return s.postGeneratedArtifacts(ctx, types.PocScheme_POC_SCHEME_PREFILL)
}

func (s *Server) postGeneratedArtifacts(ctx echo.Context, scheme types.PocScheme) error {
	if s.artifactStore == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "artifact store not configured")
	}

	var body mlnodeclient.GeneratedArtifactBatchV2

	if err := ctx.Bind(&body); err != nil {
		logging.Error("ArtifactBatchV2-callback. Failed to decode request body", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	modelID, err := decodeCallbackModelID(ctx.Param("model_id"))
	if err != nil {
		return err
	}
	logging.Debug("ArtifactBatchV2-callback. Received", types.PoC,
		"blockHeight", body.BlockHeight,
		"modelId", modelID,
		"publicKey", body.PublicKey,
		"nodeId", body.NodeId,
		"artifactsCount", len(body.Artifacts))

	epochState := s.broker.GetPhaseTracker().GetCurrentEpochState()
	if !poc.ShouldAcceptGeneratedArtifacts(epochState) {
		logging.Warn("ArtifactBatchV2-callback. Rejected - not in PoC generate phase", types.PoC,
			"blockHeight", body.BlockHeight)
		return echo.NewHTTPError(http.StatusServiceUnavailable, "not in PoC generate phase")
	}

	stageHeight := poc.GetCurrentPocStageHeight(epochState)
	if stageHeight <= 0 || body.BlockHeight != stageHeight {
		logging.Warn("ArtifactBatchV2-callback. Rejected - block height is not the active PoC stage", types.PoC,
			"blockHeight", body.BlockHeight, "activeStage", stageHeight)
		return echo.NewHTTPError(http.StatusBadRequest, "block_height is not the active PoC stage")
	}
	s.artifactStore.ActivateStage(stageHeight)

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

	recipe, err := s.callbackRecipe(body.BlockHeight, poc.GeneratingChallengeWork(epochState))
	if err != nil {
		return err
	}
	if recipe.Scheme != scheme {
		return echo.NewHTTPError(http.StatusBadRequest, "callback version does not match stage scheme")
	}
	model, ok := recipe.GetModelConfig(modelID)
	if !ok {
		return echo.NewHTTPError(http.StatusBadRequest, "model missing from stage recipe")
	}
	n, ok := model.DecodeStepsForScheme(scheme)
	if !ok {
		return echo.NewHTTPError(http.StatusBadRequest, "model missing stage scheme")
	}

	// Convert artifacts from JSON format to proto format for local storage
	protoArtifacts := make([]*types.PoCArtifactV2, 0, len(body.Artifacts))
	for _, a := range body.Artifacts {
		var vectorBytes []byte
		if scheme == types.PocScheme_POC_SCHEME_DECODE {
			vectorBytes, err = decodeArtifactVector(a, n)
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, err.Error())
			}
		} else {
			vectorBytes, err = base64.StdEncoding.DecodeString(a.VectorB64)
			if err != nil {
				logging.Error("ArtifactBatchV2-callback. Failed to decode artifact vector", types.PoC,
					"nonce", a.Nonce, "error", err)
				return echo.NewHTTPError(http.StatusBadRequest, "invalid base64 in artifact vector")
			}
		}
		if len(vectorBytes) == 0 {
			logging.Error("ArtifactBatchV2-callback. Empty artifact vector", types.PoC,
				"nonce", a.Nonce)
			return echo.NewHTTPError(http.StatusBadRequest, "empty artifact vector")
		}
		protoArtifacts = append(protoArtifacts, &types.PoCArtifactV2{
			Nonce:  int32(a.Nonce),
			Vector: vectorBytes,
		})
	}

	// Store artifacts locally for off-chain proofs
	// Store commits (MsgPoCV2StoreCommit) are submitted by CommitWorker
	// Weight distributions (MsgMLNodeWeightDistribution) are submitted at end of generation
	totalCount, nodeDistribution, err := s.addToLocalStorage(stageHeight, modelID, nodeId, protoArtifacts)
	if err != nil {
		logging.Error("ArtifactBatchV2-callback. Failed to store artifacts", types.PoC,
			"blockHeight", body.BlockHeight,
			"modelId", modelID,
			"error", err)
		return echo.NewHTTPError(http.StatusServiceUnavailable, "failed to store artifacts")
	}

	logging.Debug("ArtifactBatchV2-callback. Stored locally", types.PoC,
		"blockHeight", stageHeight,
		"modelId", modelID,
		"nodeId", nodeId,
		"artifactsCount", len(protoArtifacts),
		"totalInStore", totalCount,
		"nodeDistribution", nodeDistribution)

	return ctx.NoContent(http.StatusOK)
}

// Challenges keep their own work height but use their epoch's regular recipe.
func (s *Server) callbackRecipe(stageHeight int64, challenge *types.OpenPoCChallenge) (*types.PocStageRecipe, error) {
	var recipe *types.PocStageRecipe
	var err error
	if challenge != nil && challenge.StartHeight() == stageHeight {
		recipe, err = s.broker.EpochRecipe(challenge.Challenge.EpochIndex)
	} else {
		recipe, err = s.broker.StageRecipe(stageHeight)
	}
	if err != nil || recipe == nil {
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable, "missing poc stage recipe")
	}
	return recipe, nil
}

// postValidatedArtifactsV2 handles PoC v2 validation result callbacks from MLNode.
// Receives validation results and submits them to chain via MsgSubmitPocValidationsV2 (batch).
func (s *Server) postValidatedArtifactsV2(ctx echo.Context) error {
	return s.postValidatedArtifacts(ctx, types.PocScheme_POC_SCHEME_PREFILL)
}

func (s *Server) postValidatedArtifacts(ctx echo.Context, scheme types.PocScheme) error {
	var body mlnodeclient.ValidatedResultV2

	if err := ctx.Bind(&body); err != nil {
		logging.Error("ValidatedArtifactsV2-callback. Failed to decode request body", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	logging.Debug("ValidatedArtifactsV2-callback. Received", types.PoC,
		"blockHeight", body.BlockHeight,
		"publicKey", body.PublicKey,
		"nTotal", body.NTotal,
		"nNanSteps", body.NNanSteps,
		"nExcluded", body.NExcluded,
		"nMismatch", body.NMismatch,
		"fraudDetected", body.FraudDetected)

	modelID, err := decodeCallbackModelID(ctx.Param("model_id"))
	if err != nil {
		return err
	}

	epochState := s.broker.GetPhaseTracker().GetCurrentEpochState()
	if !poc.ShouldAcceptValidatedArtifacts(epochState) {
		logging.Warn("ValidatedArtifactsV2-callback. Rejected - not in PoC validate phase", types.PoC,
			"blockHeight", body.BlockHeight)
		return echo.NewHTTPError(http.StatusServiceUnavailable, "not in PoC validate phase")
	}

	// Convert public key to bech32 address
	// PoC validation provides hex-encoded public keys
	address, err := utils.PubKeyHexToAddress(body.PublicKey)
	if err != nil {
		logging.Error("ValidatedArtifactsV2-callback. Failed to convert public key to address", types.PoC,
			"publicKey", body.PublicKey,
			"nTotal", body.NTotal,
			"fraudDetected", body.FraudDetected,
			"error", err)
		return err
	}

	recipe, err := s.callbackRecipe(body.BlockHeight, poc.OpenChallenges.VoteFor(address, body.BlockHeight))
	if err != nil {
		return err
	}
	if recipe.Scheme != scheme {
		return echo.NewHTTPError(http.StatusBadRequest, "callback version does not match stage scheme")
	}
	if _, ok := recipe.GetModelConfig(modelID); !ok {
		return echo.NewHTTPError(http.StatusBadRequest, "model missing from stage recipe")
	}

	if body.ShouldAbstain() {
		logging.Warn("ValidatedArtifactsV2-callback. Abstaining, validator-side incomplete result", types.PoC,
			"nNanSteps", body.NNanSteps, "nExcluded", body.NExcluded, "nMismatch", body.NMismatch, "nTotal", body.NTotal)
		return ctx.NoContent(http.StatusOK)
	}

	// Convert fraud_detected + n_total to validated_weight
	validatedWeight := body.ToValidatedWeight()

	logging.Info("ValidatedArtifactsV2-callback. Submitting validation", types.PoC,
		"participant", address,
		"modelId", modelID,
		"validatedWeight", validatedWeight,
		"fraudDetected", body.FraudDetected)

	// Use batch submission (even for single validation - no single-validation RPC exists)
	if ch := poc.OpenChallenges.VoteFor(address, body.BlockHeight); ch != nil {
		msg := &types.MsgSubmitPoCChallengeValidations{
			PocStageStartBlockHeight: body.BlockHeight,
			Validations: []*types.PoCValidationEntryV2{
				{
					ParticipantAddress: address,
					ModelId:            modelID,
					ValidatedWeight:    validatedWeight,
				},
			},
		}
		if err := s.recorder.SubmitPoCChallengeValidations(msg); err != nil {
			logging.Error("ValidatedArtifactsV2-callback. Failed to submit MsgSubmitPoCChallengeValidations", types.PoC,
				"participant", address,
				"error", err)
			return err
		}
		return ctx.NoContent(http.StatusOK)
	}

	msg := &types.MsgSubmitPocValidationsV2{
		PocStageStartBlockHeight: body.BlockHeight,
		Validations: []*types.PoCValidationEntryV2{
			{
				ParticipantAddress: address,
				ModelId:            modelID,
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

func (s *Server) addToLocalStorage(pocStageStartHeight int64, modelID, nodeId string, protoArtifacts []*types.PoCArtifactV2) (uint32, map[string]uint32, error) {
	if s.artifactStore == nil {
		return 0, nil, errors.New("artifact store not configured")
	}

	store, err := s.artifactStore.GetOrCreateStore(pocStageStartHeight, modelID)
	if err != nil {
		logging.Error("Failed to get artifact store", types.PoC,
			"pocStageStartHeight", pocStageStartHeight, "modelId", modelID, "error", err)
		return 0, nil, err
	}

	for _, a := range protoArtifacts {
		if err := store.AddWithNode(int32(a.Nonce), a.Vector, nodeId); err != nil {
			if errors.Is(err, artifacts.ErrDuplicateNonce) {
				continue
			}
			logging.Error("Failed to store artifact", types.PoC, "nonce", a.Nonce, "error", err)
			return 0, nil, err
		}
	}

	return store.Count(), store.GetNodeCounts(), nil
}
