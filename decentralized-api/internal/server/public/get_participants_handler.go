package public

import (
	"common/logging"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func grpcErrorToHTTP(err error) error {
	if err == nil {
		return nil
	}

	st, ok := status.FromError(err)
	if !ok {
		return err
	}

	switch st.Code() {
	case codes.NotFound:
		return echo.NewHTTPError(http.StatusNotFound, st.Message())
	case codes.InvalidArgument:
		return echo.NewHTTPError(http.StatusBadRequest, st.Message())
	case codes.Unauthenticated:
		return echo.NewHTTPError(http.StatusUnauthorized, st.Message())
	case codes.PermissionDenied:
		return echo.NewHTTPError(http.StatusForbidden, st.Message())
	case codes.ResourceExhausted:
		return echo.NewHTTPError(http.StatusTooManyRequests, st.Message())
	case codes.Unavailable:
		return echo.NewHTTPError(http.StatusServiceUnavailable, st.Message())
	default:
		return echo.NewHTTPError(http.StatusInternalServerError, st.Message())
	}
}

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
		return grpcErrorToHTTP(err)
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
		return grpcErrorToHTTP(err)
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
