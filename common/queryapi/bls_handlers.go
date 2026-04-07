package queryapi

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"common/queryapi/gen"
)

func (h *Handlers) GetBLSEpoch(ctx echo.Context, id int) error {
	return ctx.JSON(http.StatusNotImplemented, gen.BLSEpochResponse{})
}

func (h *Handlers) GetBLSEpochs(ctx echo.Context, id int) error {
	return ctx.JSON(http.StatusNotImplemented, gen.BLSEpochResponse{})
}

func (h *Handlers) GetBLSSignature(ctx echo.Context, requestId string) error {
	return ctx.JSON(http.StatusNotImplemented, gen.BLSSignatureResponse{})
}
