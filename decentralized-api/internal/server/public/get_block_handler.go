package public

import (
	"context"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
	"strconv"
)

func (s *Server) getBlock(c echo.Context) error {
	blockHeightParam := c.Param("height")
	if blockHeightParam == "" {
		return ErrInvalidBlockHeight
	}

	blockHeight, err := strconv.ParseInt(blockHeightParam, 10, 64)
	if err != nil {
		return ErrInvalidBlockHeight
	}

	resp, err := s.recorder.NewInferenceQueryClient().GetBlockProofByHeight(context.Background(), &types.QueryBlockProofRequest{ProofHeight: blockHeight})
	if err != nil {
		logging.Error("Failed to get block proof by height", types.Participants, "error", err)
		return err
	}

	/*	rpcClient, err := cosmos_client.NewRpcClient(s.configManager.GetChainNodeConfig().Url)
		if err != nil {
			logging.Error("Failed to create rpc client", types.System, "error", err)
			return err
		}

		block, err := rpcClient.Block(c.Request().Context(), &blockHeight)
		if err != nil {
			logging.Error("Failed to get validators", types.System, "error", err)
			return err
		}*/
	return c.JSON(http.StatusOK, resp)
}

func (s *Server) getValidatorsByBlock(c echo.Context) error {
	blockHeightParam := c.Param("height")
	if blockHeightParam == "" {
		return ErrInvalidBlockHeight
	}

	blockHeight, err := strconv.ParseInt(blockHeightParam, 10, 64)
	if err != nil {
		return ErrInvalidBlockHeight
	}

	rpcClient, err := cosmos_client.NewRpcClient(s.configManager.GetChainNodeConfig().Url)
	if err != nil {
		logging.Error("Failed to create rpc client", types.System, "error", err)
		return err
	}
	validators, err := rpcClient.Validators(c.Request().Context(), &blockHeight, nil, nil)
	if err != nil {
		logging.Error("Failed to get validators", types.System, "error", err)
		return err
	}

	return c.JSON(http.StatusOK, validators)
}
