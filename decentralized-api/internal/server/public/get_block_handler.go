package public

import (
	"context"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/logging"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
	"strconv"
)

type TemporaryResponse struct {
	BlockProof           *types.BlockProof
	ValidatorsSignatures *types.ValidatorsProof
	CosmosBlock          *coretypes.ResultBlock
}

func (s *Server) getBlock(c echo.Context) error {
	blockHeightParam := c.Param("height")
	if blockHeightParam == "" {
		return ErrInvalidBlockHeight
	}

	blockHeight, err := strconv.ParseInt(blockHeightParam, 10, 64)
	if err != nil {
		return ErrInvalidBlockHeight
	}

	cl := s.recorder.NewInferenceQueryClient()
	blockProof, err := cl.GetBlockProofByHeight(context.Background(), &types.QueryBlockProofRequest{ProofHeight: blockHeight})
	if err != nil {
		logging.Error("Failed to get block proof by height", types.Participants, "error", err)
		return err
	}

	signaturesResp, err := cl.GetValidatorsProofByHeight(context.Background(), &types.QueryGetValidatorsProofRequest{ProofHeight: blockHeight})
	if err != nil {
		logging.Error("Failed to get block proof by height", types.Participants, "error", err)
	}

	rpcClient, err := cosmos_client.NewRpcClient(s.configManager.GetChainNodeConfig().Url)
	if err != nil {
		logging.Error("Failed to create rpc client", types.System, "error", err)
		return err
	}

	block, err := rpcClient.Block(c.Request().Context(), &blockHeight)
	if err != nil {
		logging.Error("Failed to get cosmos block", types.System, "error", err)
	}

	var signatures *types.ValidatorsProof
	if signaturesResp != nil {
		signatures = signaturesResp.Proof
	}

	resp := TemporaryResponse{
		BlockProof:           blockProof.Proof,
		ValidatorsSignatures: signatures,
		CosmosBlock:          block,
	}
	return c.JSON(http.StatusOK, resp)
}
