package queryapi

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	inferencetypes "github.com/productscience/inference/x/inference/types"

	"edge-api/queryapi/gen"
)

// See: decentralized-api/internal/server/public/bridge_handlers.go:375
func (h *Handlers) GetBridgeAddresses(ctx echo.Context, params gen.GetBridgeAddressesParams) error {
	resp, err := h.chain.InferenceQueryClient().BridgeAddressesByChain(
		ctx.Request().Context(),
		&inferencetypes.QueryBridgeAddressesByChainRequest{ChainId: params.Chain},
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to get addresses for chain '%s': %v", params.Chain, err))
	}

	var addresses []string
	for _, a := range resp.Addresses {
		addresses = append(addresses, a.Address)
	}
	return ctx.JSON(http.StatusOK, gen.BridgeAddressesResponse{
		ChainName: params.Chain,
		ChainId:   params.Chain,
		Addresses: addresses,
	})
}
