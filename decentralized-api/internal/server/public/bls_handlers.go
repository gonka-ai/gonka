package public

import (
	"context"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	blsTypes "github.com/productscience/inference/x/bls/types"
)

// getBLSEpochByID handles requests for BLS epoch data
func (s *Server) getBLSEpochByID(c echo.Context) error {
	idStr := c.Param("id")
	epochID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid epoch ID")
	}

	blsQueryClient := s.recorder.NewBLSQueryClient()
	res, err := blsQueryClient.EpochBLSData(context.Background(), &blsTypes.QueryEpochBLSDataRequest{
		EpochId: epochID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to query BLS epoch data: "+err.Error())
	}

	return c.JSON(http.StatusOK, res.EpochData)
}

// getBLSSignatureByRequestID handles requests for BLS signature data
func (s *Server) getBLSSignatureByRequestID(c echo.Context) error {
	requestIDHex := c.Param("request_id")
	requestIDBytes, err := hex.DecodeString(requestIDHex)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid request ID format (must be hex-encoded)")
	}

	blsQueryClient := s.recorder.NewBLSQueryClient()
	res, err := blsQueryClient.SigningStatus(context.Background(), &blsTypes.QuerySigningStatusRequest{
		RequestId: requestIDBytes,
	})
	if err != nil {
		// If the request is not found, return null instead of an error to match client expectations
		if strings.Contains(err.Error(), "not found") {
			return c.JSON(http.StatusOK, map[string]interface{}{"signing_request": nil})
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to query BLS signature data: "+err.Error())
	}

	return c.JSON(http.StatusOK, res)
}
