package public

import (
	"net/http"

	"github.com/labstack/echo/v4"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
)

type EmergencyTransferRequest struct {
	ExemptionId string `json:"exemption_id"`
	FromAddress string `json:"from_address"`
	ToAddress   string `json:"to_address"`
	Amount      string `json:"amount"`
	Denom       string `json:"denom"`
}

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

func (s *Server) postEmergencyTransfer(c echo.Context) error {
	var req EmergencyTransferRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	msg := &restrictionstypes.MsgExecuteEmergencyTransfer{
		ExemptionId: req.ExemptionId,
		FromAddress: req.FromAddress,
		ToAddress:   req.ToAddress,
		Amount:      req.Amount,
		Denom:       req.Denom,
	}

	_, err := s.recorder.SendTransactionAsyncNoRetry(msg)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to submit emergency transfer: "+err.Error())
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "submitted"})
}
