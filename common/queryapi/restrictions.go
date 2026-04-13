package queryapi

import (
	"net/http"

	"github.com/labstack/echo/v4"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
)

// Ported from decentralized-api/internal/server/public/restrictions_handlers.go:11
func (h *Handlers) GetRestrictionsStatus(ctx echo.Context) error {
	response, err := h.chain.RestrictionsQueryClient().TransferRestrictionStatus(
		ctx.Request().Context(),
		&restrictionstypes.QueryTransferRestrictionStatusRequest{},
	)
	if err != nil {
		return grpcErrorToHTTP(err)
	}
	return ctx.JSON(http.StatusOK, response)
}

// Ported from decentralized-api/internal/server/public/restrictions_handlers.go:20
func (h *Handlers) GetRestrictionsExemptions(ctx echo.Context) error {
	response, err := h.chain.RestrictionsQueryClient().TransferExemptions(
		ctx.Request().Context(),
		&restrictionstypes.QueryTransferExemptionsRequest{},
	)
	if err != nil {
		return grpcErrorToHTTP(err)
	}
	return ctx.JSON(http.StatusOK, response)
}

// Ported from decentralized-api/internal/server/public/restrictions_handlers.go:29
func (h *Handlers) GetRestrictionsExemptionUsage(ctx echo.Context, id string, account string) error {
	response, err := h.chain.RestrictionsQueryClient().ExemptionUsage(
		ctx.Request().Context(),
		&restrictionstypes.QueryExemptionUsageRequest{
			ExemptionId:    id,
			AccountAddress: account,
		},
	)
	if err != nil {
		return grpcErrorToHTTP(err)
	}
	return ctx.JSON(http.StatusOK, response)
}
