package queryapi

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"

	"common/logging"
)

// Ported from decentralized-api/internal/server/public/get_poc_batches_handler.go:12
// Change: also returns 404 for a non-nil response with an empty PocBatch slice,
// not only when the response itself is nil.
func (h *Handlers) GetPoCBatches(ctx echo.Context, epoch int64) error {
	logging.Debug("getPoCBatches", types.PoC, "epoch", epoch)
	resp, err := h.chain.InferenceQueryClient().PocBatchesForStage(
		ctx.Request().Context(),
		&inferencetypes.QueryPocBatchesForStageRequest{BlockHeight: epoch},
	)
	if err != nil {
		logging.Error("Failed to get PoC batches.", types.PoC, "epoch", epoch)
		return err
	}
	if resp == nil || len(resp.PocBatch) == 0 {
		return echo.NewHTTPError(http.StatusNotFound, fmt.Sprintf("PoC batches batches not found. epoch = %d", epoch))
	}
	return ctx.JSON(http.StatusOK, resp)
}
