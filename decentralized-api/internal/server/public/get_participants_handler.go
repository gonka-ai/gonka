package public

import (
	"common/logging"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"

	"common/queryapi"
)

func (s *Server) getParticipantByAddress(c echo.Context) error {
	address := c.Param("address")
	if address == "" {
		return ErrAddressRequired
	}

	queryClient := s.recorder.NewInferenceQueryClient()
	response, err := queryClient.Participant(c.Request().Context(), &types.QueryGetParticipantRequest{
		Index: address,
	})
	if err != nil {
		logging.Error("Failed to get participant", types.Participants, "address", address, "error", err)
		return queryapi.GRPCErrorToHTTP(err)
	}

	return c.JSON(http.StatusOK, response)
}

func (s *Server) getAccountByAddress(c echo.Context) error {
	address := c.Param("address")
	if address == "" {
		return ErrAddressRequired
	}

	queryClient := s.recorder.NewInferenceQueryClient()
	response, err := queryClient.AccountByAddress(c.Request().Context(), &types.QueryAccountByAddressRequest{
		Address: address,
	})
	if err != nil {
		logging.Error("Failed to get account", types.Participants, "address", address, "error", err)
		return queryapi.GRPCErrorToHTTP(err)
	}

	if response == nil {
		return ErrAccountNotFound
	}

	// Proto JSON skips balance when it is 0, so we return DTO.
	return c.JSON(http.StatusOK, AccountDto{
		Pubkey:  response.Pubkey,
		Balance: response.Balance,
		Denom:   response.Denom,
	})
}
