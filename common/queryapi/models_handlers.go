package queryapi

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"common/queryapi/gen"
)

func (h *Handlers) GetModels(ctx echo.Context) error {
	return ctx.JSON(http.StatusNotImplemented, gen.ModelsResponse{})
}

func (h *Handlers) GetGovernanceModels(ctx echo.Context) error {
	return ctx.JSON(http.StatusNotImplemented, gen.GovernanceModelsResponse{})
}

func (h *Handlers) GetGovernanceModelsLegacy(ctx echo.Context) error {
	return ctx.JSON(http.StatusNotImplemented, gen.ModelsLegacyResponse{})
}

func (h *Handlers) GetPricing(ctx echo.Context) error {
	return ctx.JSON(http.StatusNotImplemented, gen.PricingDto{})
}

func (h *Handlers) GetGovernancePricing(ctx echo.Context) error {
	return ctx.JSON(http.StatusNotImplemented, gen.PricingDto{})
}
