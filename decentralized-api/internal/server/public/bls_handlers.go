package public

import (
	"context"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"decentralized-api/internal/bls"

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

	// Convenience fields: uncompressed G2 group key (256 bytes) and uncompressed validation signature (128 bytes)
	var uncompressedG2 []byte
	if len(res.EpochData.GroupPublicKey) == 96 {
		uncompressedG2, _ = bls.DecompressG2To256Blst(res.EpochData.GroupPublicKey)
	}

	var uncompressedValSig []byte
	if len(res.EpochData.ValidationSignature) == 48 {
		uncompressedValSig, _ = bls.DecompressG1To128Blst(res.EpochData.ValidationSignature)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"epoch_data":                            res.EpochData,
		"group_public_key_uncompressed_256":     uncompressedG2,
		"validation_signature_uncompressed_128": uncompressedValSig,
	})
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

	// Augment response with 128-byte uncompressed G1 signature (x||y, each 64-byte big-endian) if available
	var uncompressedSig []byte
	if res != nil && res.SigningRequest.Status == blsTypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED {
		sig := res.SigningRequest.FinalSignature
		if len(sig) == 48 {
			uncompressedSig, _ = bls.DecompressG1To128Blst(sig)
		}
	}

	// Return composite JSON with original signing_request and convenience field
	return c.JSON(http.StatusOK, map[string]interface{}{
		"signing_request":            res.SigningRequest,
		"uncompressed_signature_128": uncompressedSig, // base64-encoded in JSON
	})
}
