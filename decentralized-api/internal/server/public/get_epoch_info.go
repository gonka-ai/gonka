package public

import (
	"decentralized-api/logging"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
)

func (s *Server) getEpochSummaryInfo(ctx echo.Context) error {
	epochStr := ctx.Param("epoch")
	if epochStr == "" || !(epochStr == "latest" || epochStr == "current") {
		return ErrInvalidEpochId
	}

	epochStartHeight, err := s.getEpochStartBlockHeight(epochStr)
	if err != nil {
		return ErrInvalidEpochId
	}

	participantIDs := ctx.QueryParams()["participantIds"]
	if len(participantIDs) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "no participantIds provided")
	}

	queryClient := s.recorder.NewInferenceQueryClient()
	summaries, err := queryClient.EpochPerformanceSummaryByParticipants(ctx.Request().Context(), &types.QueryParticipantsEpochPerformanceSummaryRequest{
		EpochStartHeight: epochStartHeight,
		ParticipantId:    participantIDs,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return ctx.JSON(http.StatusOK, summaries)
}

func (s *Server) getEpochStartBlockHeight(epochStr string) (uint64, error) {
	var blockStartHeight uint64

	queryClient := s.recorder.NewInferenceQueryClient()
	switch epochStr {
	case "current":
		epoch, err := queryClient.CurrentEpochGroupData(*s.recorder.GetContext(), &types.QueryCurrentEpochGroupDataRequest{})
		if err != nil {
			logging.Error("Failed to get current epoch", types.Participants, "error", err)
			return 0, err
		}

		blockStartHeight = epoch.EpochGroupData.PocStartBlockHeight
	case "latest":
		epoch, err := queryClient.PreviousEpochGroupData(*s.recorder.GetContext(), &types.QueryPreviousEpochGroupDataRequest{})
		if err != nil {
			logging.Error("Failed to get current epoch", types.Participants, "error", err)
			return 0, err
		}
		blockStartHeight = epoch.EpochGroupData.PocStartBlockHeight
	}

	/*epoch, err := strconv.ParseUint(epochStr, 10, 64)
	if err != nil {
		return ErrInvalidEpochId
	}*/
	return blockStartHeight, nil
}
