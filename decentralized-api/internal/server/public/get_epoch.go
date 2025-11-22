package public

import (
	"decentralized-api/logging"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
)

type EpochResponse struct {
	BlockHeight                int64                          `json:"block_height"`
	LatestEpoch                LatestEpochDto                 `json:"latest_epoch"`
	Phase                      types.EpochPhase               `json:"phase"`
	EpochStages                types.EpochStages              `json:"epoch_stages"`
	NextEpochStages            types.EpochStages              `json:"next_epoch_stages"`
	EpochParams                types.EpochParams              `json:"epoch_params"`
	IsConfirmationPocActive    bool                           `json:"is_confirmation_poc_active"`
	ActiveConfirmationPocEvent *types.ConfirmationPoCEvent    `json:"active_confirmation_poc_event,omitempty"`
}

// LatestEpochDto, had to indroduced it, because types.Epoch doesn't serialize when
// Index and PocStartBlockHeight are 0
type LatestEpochDto struct {
	Index               uint64 `json:"index"`
	PocStartBlockHeight int64  `json:"poc_start_block_height"`
}

func (s *Server) getEpochById(ctx echo.Context) error {
	epochParam := ctx.Param("epoch")
	if epochParam != "latest" {
		return echo.NewHTTPError(http.StatusBadRequest, "Only getting info for current epoch is supported at the moment")
	}

	queryClient := s.recorder.NewInferenceQueryClient()
	epochInfo, err := queryClient.EpochInfo(ctx.Request().Context(), &types.QueryEpochInfoRequest{})
	if err != nil {
		logging.Error("Failed to get latest epoch info", types.EpochGroup, "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	epochParams := *epochInfo.Params.EpochParams

	epochContext := types.NewEpochContext(epochInfo.LatestEpoch, epochParams)
	nextEpochContext := epochContext.NextEpochContext()

	response := EpochResponse{
		BlockHeight: epochInfo.BlockHeight,
		LatestEpoch: LatestEpochDto{
			Index:               epochInfo.LatestEpoch.Index,
			PocStartBlockHeight: epochInfo.LatestEpoch.PocStartBlockHeight,
		},
		Phase:                      epochContext.GetCurrentPhase(epochInfo.BlockHeight),
		EpochStages:                epochContext.GetEpochStages(),
		NextEpochStages:            nextEpochContext.GetEpochStages(),
		EpochParams:                *epochInfo.Params.EpochParams,
		IsConfirmationPocActive:    epochInfo.IsConfirmationPocActive,
		ActiveConfirmationPocEvent: epochInfo.ActiveConfirmationPocEvent,
	}
	return ctx.JSON(http.StatusOK, response)
}
