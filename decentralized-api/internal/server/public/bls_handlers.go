package public

import (
	"context"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fp"
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
		var g2 bls12381.G2Affine
		if err := g2.Unmarshal(res.EpochData.GroupPublicKey); err == nil {
			appendFp64 := func(e fp.Element, dst *[]byte) {
				be48 := e.Bytes()
				var limb [64]byte
				copy(limb[64-48:], be48[:])
				*dst = append(*dst, limb[:]...)
			}
			// Order: X.c0, X.c1, Y.c0, Y.c1
			appendFp64(g2.X.A0, &uncompressedG2)
			appendFp64(g2.X.A1, &uncompressedG2)
			appendFp64(g2.Y.A0, &uncompressedG2)
			appendFp64(g2.Y.A1, &uncompressedG2)
		}
	}

	var uncompressedValSig []byte
	if len(res.EpochData.ValidationSignature) == 48 {
		var g1 bls12381.G1Affine
		if err := g1.Unmarshal(res.EpochData.ValidationSignature); err == nil {
			appendFp64 := func(e fp.Element, dst *[]byte) {
				be48 := e.Bytes()
				var limb [64]byte
				copy(limb[64-48:], be48[:])
				*dst = append(*dst, limb[:]...)
			}
			appendFp64(g1.X, &uncompressedValSig)
			appendFp64(g1.Y, &uncompressedValSig)
		}
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
			var g1 bls12381.G1Affine
			if err := g1.Unmarshal(sig); err == nil {
				// Build 64-byte big-endian limbs from 48-byte field elements
				appendFp64 := func(e fp.Element, dst *[]byte) {
					be48 := e.Bytes()
					var limb [64]byte
					copy(limb[64-48:], be48[:])
					*dst = append(*dst, limb[:]...)
				}
				appendFp64(g1.X, &uncompressedSig)
				appendFp64(g1.Y, &uncompressedSig)
			}
		}
	}

	// Return composite JSON with original signing_request and convenience field
	return c.JSON(http.StatusOK, map[string]interface{}{
		"signing_request":            res.SigningRequest,
		"uncompressed_signature_128": uncompressedSig, // base64-encoded in JSON
	})
}
