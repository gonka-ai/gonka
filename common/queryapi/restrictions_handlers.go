package queryapi

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

func (h *Handlers) GetRestrictionsStatus(ctx echo.Context) error {
	return ctx.JSON(http.StatusNotImplemented, nil)
}

func (h *Handlers) GetRestrictionsExemptions(ctx echo.Context) error {
	return ctx.JSON(http.StatusNotImplemented, nil)
}

func (h *Handlers) GetRestrictionsExemptionUsage(ctx echo.Context, id string, account string) error {
	return ctx.JSON(http.StatusNotImplemented, nil)
}

func (h *Handlers) GetTrainingTasks(ctx echo.Context) error {
	return ctx.JSON(http.StatusNotImplemented, nil)
}

func (h *Handlers) GetTrainingTask(ctx echo.Context, id int) error {
	return ctx.JSON(http.StatusNotImplemented, nil)
}
