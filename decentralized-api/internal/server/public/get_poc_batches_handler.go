package public

import (
	"decentralized-api/logging"
	"fmt"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
	"strconv"
)

func (s *Server) getPoCBatches(c echo.Context) error {
	epoch := c.Param("epoch")
	logging.Debug("getPoCBatches", types.PoC, "epoch", epoch)

	value, err := strconv.ParseInt(epoch, 10, 64)
	if err != nil {
		logging.Error("Failed to parse epoch", types.PoC, "error", err)
		return ErrInvalidEpochId
	}

	logging.Debug("Requesting PoC batches.", types.PoC, "epoch", value)

	queryClient := s.recorder.NewInferenceQueryClient()
	response, err := queryClient.PocBatchesForStage(s.recorder.GetContext(), &types.QueryPocBatchesForStageRequest{
		BlockHeight: value,
	})
	if err != nil {
		logging.Error("Failed to get PoC batches.", types.PoC, "epoch", value)
		return err
	}

	if response == nil {
		logging.Error("PoC batches batches not found", types.PoC, "epoch", value)
		msg := fmt.Sprintf("PoC batches batches not found. epoch = %d", value)
		return echo.NewHTTPError(http.StatusNotFound, msg)
	}

	return c.JSON(http.StatusOK, response)
}
