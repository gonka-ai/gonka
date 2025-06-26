package public

import (
	"decentralized-api/logging"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
)

type EpochResponse struct {
	BlockHeight int64             `json:"block_height"`
	LatestEpoch types.Epoch       `json:"latest_epoch"`
	Phase       types.EpochPhase  `json:"phase"`
	EpochParams types.EpochParams `json:"epoch_params"`
}

func (s *Server) getEpochById(ctx echo.Context) error {
	epochParam := ctx.Param("epoch")
	if epochParam != "current" {
		return echo.NewHTTPError(http.StatusBadRequest, "Only getting info for current epoch is supported at the moment")
	}

	queryClient := s.recorder.NewInferenceQueryClient()
	epochInfo, err := queryClient.EpochInfo(ctx.Request().Context(), &types.QueryEpochInfoRequest{})
	if err != nil {
		logging.Error("Failed to get current epoch info", types.EpochGroup, "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	epochParams := *epochInfo.Params.EpochParams

	epochContext := types.NewEpochContext(epochInfo.LatestEpoch, epochParams)

	response := EpochResponse{
		BlockHeight: epochInfo.BlockHeight,
		LatestEpoch: epochInfo.LatestEpoch,
		Phase:       epochContext.GetCurrentPhase(epochInfo.BlockHeight),
		EpochParams: *epochInfo.Params.EpochParams,
	}
	return ctx.JSON(http.StatusOK, response)
}
