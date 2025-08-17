package public

import (
	"net/http"

	"github.com/labstack/echo/v4"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
)

// Query-only handlers for restrictions module
func (s *Server) getRestrictionsStatus(c echo.Context) error {
	queryClient := s.recorder.NewRestrictionsQueryClient()
	response, err := queryClient.TransferRestrictionStatus(c.Request().Context(), &restrictionstypes.QueryTransferRestrictionStatusRequest{})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, response)
}

func (s *Server) getRestrictionsExemptions(c echo.Context) error {
	queryClient := s.recorder.NewRestrictionsQueryClient()
	response, err := queryClient.TransferExemptions(c.Request().Context(), &restrictionstypes.QueryTransferExemptionsRequest{})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, response)
}

func (s *Server) getRestrictionsExemptionUsage(c echo.Context) error {
	id := c.Param("id")
	account := c.Param("account")
	queryClient := s.recorder.NewRestrictionsQueryClient()
	response, err := queryClient.ExemptionUsage(c.Request().Context(), &restrictionstypes.QueryExemptionUsageRequest{
		ExemptionId:    id,
		AccountAddress: account,
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, response)
}
