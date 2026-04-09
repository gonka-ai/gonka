package queryapi

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"

	cmtcrypto "github.com/cometbft/cometbft/crypto"
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/golang/protobuf/proto"
	"github.com/labstack/echo/v4"
	inferencetypes "github.com/productscience/inference/x/inference/types"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"common/chain"
	"common/logging"
	"common/queryapi/gen"
)

// Ported from decentralized-api/internal/server/public/get_epoch.go:28
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
	return ctx.JSON(http.StatusOK, gen.EpochResponse{
		BlockHeight: gen.Int64(epochInfo.BlockHeight),
		LatestEpoch: gen.LatestEpoch{
			Index:               gen.Uint64(epochInfo.LatestEpoch.Index),
			PocStartBlockHeight: gen.Int64(epochInfo.LatestEpoch.PocStartBlockHeight),
		},
		Phase:                      string(epochContext.GetCurrentPhase(epochInfo.BlockHeight)),
		EpochStages:                epochContext.GetEpochStages(),
		NextEpochStages:            nextEpochContext.GetEpochStages(),
		EpochParams:                epochInfo.Params,
		IsConfirmationPocActive:    epochInfo.IsConfirmationPocActive,
		ActiveConfirmationPocEvent: epochInfo.ActiveConfirmationPocEvent,
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

	// Query ActiveParticipants from the chain store with merkle proof.
	result, err := h.chain.CometServiceClient().ABCIQuery(ctx, &cmtservice.ABCIQueryRequest{
		Path:  "store/inference/key",
		Data:  inferencetypes.ActiveParticipantsFullKey(epoch),
		Prove: true,
	})
	if err != nil {
		logging.Error("Failed to query active participants", inferencetypes.Participants, "error", err)
		return nil, err
	}
	if result.Code != 0 {
		logging.Error("ABCI query failed", inferencetypes.Participants, "code", result.Code, "log", result.Log)
		return nil, echo.NewHTTPError(http.StatusNotFound, "active participants not found for epoch")
	}

	var activeParticipants inferencetypes.ActiveParticipants
	if err := proto.Unmarshal(result.Value, &activeParticipants); err != nil {
		logging.Error("Failed to unmarshal active participants", inferencetypes.Participants, "error", err)
		return nil, err
	}
	logging.Debug("Active participants retrieved", inferencetypes.Participants,
		"epoch", epoch,
		"count", len(activeParticipants.Participants))

	// Block at creation height and block+1 (which commits the app hash for block N).
	// blockResp, err := h.chain.CometServiceClient().GetBlockByHeight(ctx, &cmtservice.GetBlockByHeightRequest{
	// 	Height: activeParticipants.CreatedAtBlockHeight,
	// })
	// if err != nil {
	// 	logging.Error("Failed to get block", inferencetypes.Participants, "error", err)
	// 	return nil, err
	// }
	// _ = blockResp // available for proof verification if needed

	heightP1 := activeParticipants.CreatedAtBlockHeight + 1
	blockP1Resp, err := h.chain.CometServiceClient().GetBlockByHeight(ctx, &cmtservice.GetBlockByHeightRequest{
		Height: heightP1,
	})
	if err != nil {
		// Non-fatal: the block+1 may not exist yet; proceed without it.
		logging.Error("Failed to get block+1", inferencetypes.Participants, "error", err)
	}

	// Validators at the creation height.
	valsResp, err := h.chain.CometServiceClient().GetValidatorSetByHeight(ctx, &cmtservice.GetValidatorSetByHeightRequest{
		Height: activeParticipants.CreatedAtBlockHeight,
	})
	if err != nil {
		logging.Error("Failed to get validators", inferencetypes.Participants, "error", err)
		return nil, err
	}

	// Derive bech32 consensus addresses from participant validator keys.
	addresses := make([]string, len(activeParticipants.Participants))
	for i, participant := range activeParticipants.Participants {
		addr, err := validatorKeyToAddress(participant.ValidatorKey)
		if err != nil {
			logging.Error("Failed to derive address from validator key", inferencetypes.Participants,
				"key", participant.ValidatorKey, "error", err)
		}
		addresses[i] = addr
	}

	validators := make([]gen.RawProtoJson, len(valsResp.Validators))
	for i, v := range valsResp.Validators {
		validators[i] = v
	}

	var block gen.RawProtoJson
	if blockP1Resp != nil {
		block = blockP1Resp.SdkBlock
	}

	var proofOps gen.RawProtoJson
	if result.ProofOps != nil {
		proofOps = result.ProofOps
	}

	return &gen.ActiveParticipantWithProof{
		ActiveParticipants:      activeParticipants,
		Addresses:               addresses,
		ActiveParticipantsBytes: hex.EncodeToString(result.Value),
		ProofOps:                &proofOps,
		Validators:              validators,
		Block:                   &block,
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

// validatorKeyToAddress derives a bech32 consensus address from a hex- or
// base64-encoded validator public key stored in ActiveParticipant.ValidatorKey.
func validatorKeyToAddress(validatorKey string) (string, error) {
	keyBz, err := hex.DecodeString(validatorKey)
	if err != nil {
		// Fall back to base64 (standard or raw).
		keyBz, err = base64.StdEncoding.DecodeString(validatorKey)
		if err != nil {
			keyBz, err = base64.RawStdEncoding.DecodeString(validatorKey)
			if err != nil {
				return "", fmt.Errorf("cannot decode validator key %q: %w", validatorKey, err)
			}
		}
	}
	return sdk.ConsAddress(cmtcrypto.AddressHash(keyBz)).String(), nil
}
