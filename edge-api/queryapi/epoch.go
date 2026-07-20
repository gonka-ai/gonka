package queryapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	cryptotypes "github.com/cometbft/cometbft/proto/tendermint/crypto"
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/golang/protobuf/proto"
	"github.com/labstack/echo/v4"
	inferencetypes "github.com/productscience/inference/x/inference/types"

	"common/chain"
	"common/logging"
	"common/observability"
	"edge-api/queryapi/gen"
	"common/utils"
)

// See: decentralized-api/internal/server/public/get_epoch.go:28
func (h *Handlers) GetEpoch(ctx echo.Context, epoch string) error {
	if epoch != "latest" {
		return echo.NewHTTPError(http.StatusBadRequest, "Only getting info for current epoch is supported at the moment")
	}
	epochInfo, err := h.chain.InferenceQueryClient().EpochInfo(
		ctx.Request().Context(),
		&inferencetypes.QueryEpochInfoRequest{},
	)
	if err != nil {
		return grpcErrorToHTTP(err)
	}

	epochContext := inferencetypes.NewEpochContext(epochInfo.LatestEpoch, *epochInfo.Params.EpochParams)
	nextEpochContext := epochContext.NextEpochContext()
	epochParams, err := protoToRawJSON(&epochInfo.Params)
	if err != nil {
		logging.Error("Failed to encode epoch params", inferencetypes.Server, "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to encode epoch params")
	}
	activeConfirmationPoc, err := protoToRawJSON(epochInfo.ActiveConfirmationPocEvent)
	if err != nil {
		logging.Error("Failed to encode confirmation PoC event", inferencetypes.Server, "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to encode confirmation PoC event")
	}
	return ctx.JSON(http.StatusOK, gen.EpochResponse{
		BlockHeight: gen.Int64(epochInfo.BlockHeight),
		LatestEpoch: gen.LatestEpoch{
			Index:               gen.Uint64(epochInfo.LatestEpoch.Index),
			PocStartBlockHeight: gen.Int64(epochInfo.LatestEpoch.PocStartBlockHeight),
		},
		Phase:                      string(epochContext.GetCurrentPhase(epochInfo.BlockHeight)),
		EpochStages:                epochContext.GetEpochStages(),
		NextEpochStages:            nextEpochContext.GetEpochStages(),
		EpochParams:                epochParams,
		IsConfirmationPocActive:    epochInfo.IsConfirmationPocActive,
		ActiveConfirmationPocEvent: activeConfirmationPoc,
	})
}

// Ported from decentralized-api/internal/server/public/get_participants_handler.go:102
// Changes:
//   - Chain query uses CometServiceClient.ABCIQuery (gRPC) instead of the old Tendermint RPC helper.
//   - Participant addresses are returned as bech32 consensus addresses instead of hex-uppercase.
func (h *Handlers) GetEpochParticipants(ctx echo.Context, epochString string) error {
	qc := h.chain.InferenceQueryClient()
	epoch, err := getEpochFromParam(ctx.Request().Context(), epochString, qc)
	if err != nil {
		logging.Error("Failed to resolve epoch from context", inferencetypes.Server, "error", err)
		return err
	}
	resp, err := h.getEpochParticipants(ctx.Request().Context(), epoch)
	if err != nil {
		return grpcErrorToHTTP(err)
	}
	return ctx.JSON(http.StatusOK, resp)
}

func getEpochFromParam(ctx context.Context, e string, qc chain.InferenceClient) (uint64, error) {
	if e == "" {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "Invalid epoch id")
	}
	if e != "current" {
		epochToProcess, err := strconv.ParseUint(e, 10, 64)
		if err != nil {
			return 0, echo.NewHTTPError(http.StatusBadRequest, "Invalid epoch id")
		}
		return epochToProcess, nil
	}
	currEpoch, err := qc.GetCurrentEpoch(ctx, &inferencetypes.QueryGetCurrentEpochRequest{})
	if err != nil {
		logging.Error("Failed to get current epoch", inferencetypes.Participants, "error", err)
		return 0, grpcErrorToHTTP(err)
	}
	logging.Info("Current epoch resolved.", inferencetypes.Participants, "epoch", currEpoch.Epoch)
	return currEpoch.Epoch, nil
}

func (h *Handlers) getEpochParticipants(ctx context.Context, epoch uint64) (*gen.ActiveParticipantWithProof, error) {
	if epoch == 0 {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "Epoch enumeration starts with 1")
	}

	// First query without proof to read CreatedAtBlockHeight from the value.
	// A second query anchors the proof at that specific height so it verifies
	// against the block+1 header rather than the latest state root.
	valueCtx, valueOp := observability.Chain.StartStoreQuery(ctx, "inference", false, 0)
	var valueErr error
	defer valueOp.FinishErr(&valueErr)

	valueResult, err := h.chain.CometServiceClient().ABCIQuery(valueCtx, &cmtservice.ABCIQueryRequest{
		Path: "store/inference/key",
		Data: inferencetypes.ActiveParticipantsFullKey(epoch),
	})
	if err != nil {
		valueErr = err
		logging.Error("Failed to query active participants", inferencetypes.Participants, "error", err)
		return nil, err
	}
	if valueResult.Code != 0 {
		logging.Error("ABCI query failed", inferencetypes.Participants, "code", valueResult.Code, "log", valueResult.Log)
		return nil, echo.NewHTTPError(http.StatusNotFound, "active participants not found for epoch")
	}
	if len(valueResult.Value) == 0 {
		return nil, echo.NewHTTPError(http.StatusNotFound, "active participants not found for epoch")
	}

	var activeParticipants inferencetypes.ActiveParticipants
	if err := proto.Unmarshal(valueResult.Value, &activeParticipants); err != nil {
		logging.Error("Failed to unmarshal active participants", inferencetypes.Participants, "error", err)
		return nil, err
	}
	logging.Debug("Active participants retrieved", inferencetypes.Participants,
		"epoch", epoch,
		"count", len(activeParticipants.Participants))

	// Re-query at CreatedAtBlockHeight with proof so the proof is anchored to the
	// correct historical state root. Block+1 commits the app hash for that height.
	proofHeight := activeParticipants.CreatedAtBlockHeight
	proofCtx, proofOp := observability.Chain.StartStoreQuery(ctx, "inference", true, proofHeight)
	var proofErr error
	defer proofOp.FinishErr(&proofErr)

	result, err := h.chain.CometServiceClient().ABCIQuery(proofCtx, &cmtservice.ABCIQueryRequest{
		Path:   "store/inference/key",
		Data:   inferencetypes.ActiveParticipantsFullKey(epoch),
		Height: proofHeight,
		Prove:  true,
	})
	if err != nil {
		proofErr = err
		logging.Error("Failed to query active participants with proof", inferencetypes.Participants, "error", err)
		return nil, err
	}
	if result.Code != 0 {
		logging.Error("ABCI proof query failed", inferencetypes.Participants, "code", result.Code, "log", result.Log)
		return nil, echo.NewHTTPError(http.StatusNotFound, "active participants not found for epoch")
	}

	heightP1 := activeParticipants.CreatedAtBlockHeight + 1
	blockP1Resp, err := h.chain.CometServiceClient().GetBlockByHeight(ctx, &cmtservice.GetBlockByHeightRequest{
		Height: heightP1,
	})
	if err != nil {
		// Non-fatal: block+1 may not exist yet for the current epoch.
		logging.Error("Failed to get block+1", inferencetypes.Participants, "error", err)
	}

	// Non-fatal server-side proof verification: confirms the returned value is
	// anchored to the committed app hash at CreatedAtBlockHeight+1.
	if result.ProofOps != nil && blockP1Resp != nil {
		if bz, err := json.Marshal(result.ProofOps); err == nil {
			var cryptoProofOps cryptotypes.ProofOps
			if err := json.Unmarshal(bz, &cryptoProofOps); err == nil {
				dataKey := inferencetypes.ActiveParticipantsFullKey(epoch)
				verKey := "/inference/" + url.PathEscape(string(dataKey))
				appHash := blockP1Resp.SdkBlock.Header.AppHash
				if err := utils.VerifyUsingProofRt(&cryptoProofOps, appHash, verKey, result.Value); err != nil {
					logging.Error("VerifyUsingProofRt failed", inferencetypes.Participants, "error", err)
				}
				if err := utils.VerifyUsingMerkleProof(&cryptoProofOps, appHash, "inference", string(dataKey), result.Value); err != nil {
					logging.Error("VerifyUsingMerkleProof failed", inferencetypes.Participants, "error", err)
				}
			}
		}
	}

	// Validators at the creation height.
	valsResp, err := h.chain.CometServiceClient().GetValidatorSetByHeight(ctx, &cmtservice.GetValidatorSetByHeightRequest{
		Height: activeParticipants.CreatedAtBlockHeight,
	})
	if err != nil {
		logging.Error("Failed to get validators", inferencetypes.Participants, "error", err)
		return nil, err
	}

	// Derive participant addresses from validator keys.
	addresses := make([]string, len(activeParticipants.Participants))
	for i, participant := range activeParticipants.Participants {
		addr, err := validatorKeyToAddress(participant.ValidatorKey)
		if err != nil {
			logging.Error("Failed to derive address from validator key", inferencetypes.Participants,
				"key", participant.ValidatorKey, "error", err)
		}
		addresses[i] = addr
	}

	activeParticipantsJSON, err := protoToRawJSON(&activeParticipants)
	if err != nil {
		logging.Error("Failed to encode active participants", inferencetypes.Participants, "error", err)
		return nil, err
	}

	validators, err := validatorsToRawJSON(valsResp.Validators)
	if err != nil {
		logging.Error("Failed to encode validators", inferencetypes.Participants, "error", err)
		return nil, err
	}

	var block *gen.RawProtoJson
	if blockP1Resp != nil {
		block, err = protoToRawJSONPtr(blockP1Resp.SdkBlock)
		if err != nil {
			logging.Error("Failed to encode block", inferencetypes.Participants, "error", err)
			return nil, err
		}
	}

	proofOps, err := protoToRawJSONPtr(result.ProofOps)
	if err != nil {
		logging.Error("Failed to encode proof ops", inferencetypes.Participants, "error", err)
		return nil, err
	}

	return &gen.ActiveParticipantWithProof{
		ActiveParticipants:      activeParticipantsJSON,
		Addresses:               addresses,
		ActiveParticipantsBytes: hex.EncodeToString(result.Value),
		ProofOps:                proofOps,
		Validators:              validators,
		Block:                   block,
		ExcludedParticipants:    h.getExcludedParticipants(ctx, epoch),
	}, nil
}

func (h *Handlers) getExcludedParticipants(ctx context.Context, epoch uint64) []gen.ExcludedParticipant {
	excluded, err := h.chain.InferenceQueryClient().ExcludedParticipants(ctx,
		&inferencetypes.QueryExcludedParticipantsRequest{EpochIndex: epoch},
	)
	if err != nil {
		logging.Error("Failed to get excluded participants", inferencetypes.Participants, "error", err)
		return []gen.ExcludedParticipant{}
	}

	result := make([]gen.ExcludedParticipant, len(excluded.Items))
	for i, p := range excluded.Items {
		result[i] = gen.ExcludedParticipant{
			Address:              p.Address,
			Reason:               p.Reason,
			ExclusionBlockHeight: int(p.ExclusionBlockHeight),
		}
	}
	logging.Debug("Retrieved excluded participants", inferencetypes.Participants, "count", len(result))
	return result
}

func validatorKeyToAddress(validatorKey string) (string, error) {
	return utils.ValidatorKeyToHexAddress(validatorKey)
}
