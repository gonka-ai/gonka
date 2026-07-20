package queryapi

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"cosmossdk.io/errors"
	"github.com/cometbft/cometbft/crypto/ed25519"
	cryptotypes "github.com/cometbft/cometbft/proto/tendermint/crypto"
	comettypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	cosmosed25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	proto "github.com/cosmos/gogoproto/proto"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"

	"common/logging"
	"edge-api/queryapi/gen"
	"common/utils"
)

// Ported from decentralized-api/internal/server/public/active_participants_verification_handlers.go:20
// Change: verification failure returns 400 Bad Request; original returned the raw error (500 in Echo).
func (h *Handlers) PostVerifyProof(ctx echo.Context) error {
	var req gen.ProofVerificationRequest
	if err := ctx.Bind(&req); err != nil {
		logging.Error("Error decoding request", types.Participants, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	proofOpsJSON, err := json.Marshal(req.ProofOps)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, errors.Wrap(err, "invalid proof_ops"))
	}
	var proofOps cryptotypes.ProofOps
	if err := json.Unmarshal(proofOpsJSON, &proofOps); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, errors.Wrap(err, "invalid proof_ops"))
	}

	dataKey := string(inferencetypes.ActiveParticipantsFullKey(uint64(req.Epoch)))
	verKey := "/inference/" + url.PathEscape(dataKey)

	appHash, err := hex.DecodeString(req.AppHash)
	if err != nil {
		logging.Error("Error decoding app hash", types.Participants, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, errors.Wrap(err, "Error decoding app hash"))
	}

	value, err := hex.DecodeString(req.Value)
	if err != nil {
		logging.Error("Error decoding value", types.Participants, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, errors.Wrap(err, "Error decoding value"))
	}

	logging.Info("Attempting verification", types.Participants, "verKey", verKey, "appHash", appHash, "value", req.Value)

	if err := utils.VerifyUsingProofRt(&proofOps, appHash, verKey, value); err != nil {
		logging.Info("VerifyUsingProofRt failed", types.Participants, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("proof verification failed: %v", err))
	}
	return ctx.NoContent(http.StatusOK)
}

// Ported from decentralized-api/internal/server/public/active_participants_verification_handlers.go:52
// Change: verification failures return 400 Bad Request; original returned raw errors (500 in Echo).
func (h *Handlers) PostVerifyBlock(ctx echo.Context) error {
	var req gen.VerifyBlockRequest
	if err := ctx.Bind(&req); err != nil {
		logging.Error("Error decoding request", types.Participants, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	blockJson, err := json.Marshal(req.Block)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, errors.Wrap(err, "invalid Block"))
	}
	var block comettypes.Block
	if err := json.Unmarshal(blockJson, &block); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, errors.Wrap(err, "invalid Block"))
	}
	// block := &blockVerificationRequest.Block
	valSet := make([]*comettypes.Validator, len(req.Validators))
	for i, validator := range req.Validators {
		pubKeyBytes, err := base64.StdEncoding.DecodeString(validator.PubKey)
		if err != nil {
			logging.Error("Error decoding public key", types.Participants, "error", err)
			return echo.NewHTTPError(http.StatusBadRequest, errors.Wrap(err, "Error decoding public key"))
		}

		pubKey := ed25519.PubKey(pubKeyBytes)
		valSet[i] = comettypes.NewValidator(pubKey, validator.VotingPower)
	}

	groundTruth, err := h.validatorsAtHeight(ctx, block.Height)
	if err != nil {
		logging.Error("Debug block verification failed!", types.Participants, "error", err)
		return grpcErrorToHTTP(err)
	}
	logging.Info("Ground truth validators", types.Participants, "height", block.Height, "valSet", groundTruth)

	if err := utils.VerifyCommit(block.Header.ChainID, block.LastCommit, block.Header.Height, groundTruth); err != nil {
		logging.Error("Block signature verification failed (ground truth)", types.Participants, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("ground truth block verification failed: %v", err))
	}

	logging.Info("Received validators", types.Participants, "height", block.Height, "valSet", valSet)

	if err := utils.VerifyCommit(block.Header.ChainID, block.LastCommit, block.Header.Height, valSet); err != nil {
		logging.Error("Block signature verification failed", types.Participants, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("block signature verification failed: %v", err))
	}
	return ctx.NoContent(http.StatusOK)
}

// validatorsAtHeight fetches the validator set at the given height via gRPC.
func (h *Handlers) validatorsAtHeight(ctx echo.Context, height int64) ([]*comettypes.Validator, error) {
	resp, err := h.chain.CometServiceClient().GetValidatorSetByHeight(
		ctx.Request().Context(),
		&cmtservice.GetValidatorSetByHeightRequest{Height: height},
	)
	if err != nil {
		return nil, err
	}
	validators := make([]*comettypes.Validator, 0, len(resp.Validators))
	for _, v := range resp.Validators {
		var sdkPubKey cosmosed25519.PubKey
		if err := proto.Unmarshal(v.PubKey.Value, &sdkPubKey); err != nil {
			return nil, fmt.Errorf("invalid validator pubkey: %w", err)
		}
		validators = append(validators, comettypes.NewValidator(ed25519.PubKey(sdkPubKey.Key), v.VotingPower))
	}
	return validators, nil
}
