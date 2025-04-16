package public

import (
	"context"
	"cosmossdk.io/errors"
	"decentralized-api/logging"
	"decentralized-api/merkleproof"
	"encoding/base64"
	"encoding/hex"
	cmcryptoed "github.com/cometbft/cometbft/crypto/ed25519"
	rpcclient "github.com/cometbft/cometbft/rpc/client/http"
	comettypes "github.com/cometbft/cometbft/types"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
	"net/url"
)

func (s *Server) postVerifyProof(ctx echo.Context) error {
	var proofVerificationRequest ProofVerificationRequest
	if err := ctx.Bind(&proofVerificationRequest); err != nil {
		logging.Error("Error decoding request", types.Participants, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	dataKey := string(types.ActiveParticipantsFullKey(uint64(proofVerificationRequest.Epoch)))
	verKey := "/inference/" + url.PathEscape(dataKey)

	appHash, err := hex.DecodeString(proofVerificationRequest.AppHash)
	if err != nil {
		logging.Error("Error decoding app hash", types.Participants, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, errors.Wrap(err, "Error decoding app hash"))
	}

	value, err := hex.DecodeString(proofVerificationRequest.Value)
	if err != nil {
		logging.Error("Error decoding value", types.Participants, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, errors.Wrap(err, "Error decoding value"))
	}

	logging.Info("Attempting verification", types.Participants, "verKey", verKey, "appHash", appHash, "value", proofVerificationRequest.Value)

	err = merkleproof.VerifyUsingProofRt(&proofVerificationRequest.ProofOps, appHash, verKey, value)
	if err != nil {
		logging.Info("VerifyUsingProofRt failed", types.Participants, "error", err)
		return err
	}
	return ctx.NoContent(http.StatusOK)
}

func (s *Server) postVerifyBlock(ctx echo.Context) error {
	var blockVerificationRequest VerifyBlockRequest
	if err := ctx.Bind(&blockVerificationRequest); err != nil {
		logging.Error("Error decoding request", types.Participants, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	block := &blockVerificationRequest.Block
	valSet := make([]*comettypes.Validator, len(blockVerificationRequest.Validators))
	for i, validator := range blockVerificationRequest.Validators {
		pubKeyBytes, err := base64.StdEncoding.DecodeString(validator.PubKey)
		if err != nil {
			logging.Error("Error decoding public key", types.Participants, "error", err)
			return echo.NewHTTPError(http.StatusBadRequest, errors.Wrap(err, "Error decoding public key"))
		}

		pubKey := cmcryptoed.PubKey(pubKeyBytes)
		valSet[i] = comettypes.NewValidator(pubKey, validator.VotingPower)
	}

	err := debug(s.configManager.GetChainNodeConfig().Url, block)
	if err != nil {
		logging.Error("Debug block verification failed!", types.Participants, "error", err)
		return err
	}

	logging.Info("Received validators", types.Participants, "height", block.Height, "valSet", valSet)

	err = merkleproof.VerifyCommit(block.Header.ChainID, block.LastCommit, &block.Header, valSet)
	if err != nil {
		logging.Error("Block signature verification failed", types.Participants, "error", err)
		return err
	}
	return ctx.NoContent(http.StatusOK)
}

func debug(address string, block *comettypes.Block) error {
	rpcClient, err := rpcclient.New(address, "/websocket")
	if err != nil {
		return err
	}

	valSetRes, err := rpcClient.Validators(context.Background(), &block.Height, nil, nil)
	if err != nil {
		return err
	}
	valSet := valSetRes.Validators
	logging.Info("Ground truth validators", types.Participants, "height", block.Height, "valSet", valSet)

	return merkleproof.VerifyCommit(block.Header.ChainID, block.LastCommit, &block.Header, valSet)
}
