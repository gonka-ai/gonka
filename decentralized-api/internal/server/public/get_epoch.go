package public

import (
	"net/http"

	"decentralized-api/logging"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

type EpochResponse struct {
	BlockHeight                int64                       `json:"block_height"`
	LatestEpoch                LatestEpochDto              `json:"latest_epoch"`
	Phase                      types.EpochPhase            `json:"phase"`
	EpochStages                types.EpochStages           `json:"epoch_stages"`
	NextEpochStages            types.EpochStages           `json:"next_epoch_stages"`
	EpochParams                types.EpochParams           `json:"epoch_params"`
	EffectiveEpochIndex        uint64                      `json:"effective_epoch_index,omitempty"`
	IsConfirmationPocActive    bool                        `json:"is_confirmation_poc_active"`
	ActiveConfirmationPocEvent *types.ConfirmationPoCEvent `json:"active_confirmation_poc_event,omitempty"`
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

	epochParams := types.EpochParams{}
	if epochInfo.Params.EpochParams != nil {
		epochParams = *epochInfo.Params.EpochParams
	}

	// Chain EpochInfo is the source of truth for phase/stages. Local EpochContext
	// derivation remains only as a fallback for nodes that have not upgraded yet.
	phase := types.EpochPhase(epochInfo.Phase)
	epochStages := epochInfo.EpochStages
	nextEpochStages := epochInfo.NextEpochStages
	if phase == "" {
		epochContext := types.NewEpochContext(epochInfo.LatestEpoch, epochParams)
		nextEpochContext := epochContext.NextEpochContext()
		phase = epochContext.GetCurrentPhase(epochInfo.BlockHeight)
		epochStages = epochContext.GetEpochStages()
		nextEpochStages = nextEpochContext.GetEpochStages()
	}

	response := EpochResponse{
		BlockHeight: epochInfo.BlockHeight,
		LatestEpoch: LatestEpochDto{
			Index:               epochInfo.LatestEpoch.Index,
			PocStartBlockHeight: epochInfo.LatestEpoch.PocStartBlockHeight,
		},
		Phase:                      phase,
		EpochStages:                epochStages,
		NextEpochStages:            nextEpochStages,
		EpochParams:                epochParams,
		EffectiveEpochIndex:        epochInfo.EffectiveEpochIndex,
		IsConfirmationPocActive:    epochInfo.IsConfirmationPocActive,
		ActiveConfirmationPocEvent: epochInfo.ActiveConfirmationPocEvent,
	}
	return ctx.JSON(http.StatusOK, response)
}
