package queryapi

import (
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	blstypes "github.com/productscience/inference/x/bls/types"
	blst "github.com/supranational/blst/bindings/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"edge-api/queryapi/gen"
)

// decompressG1To128 converts a 48-byte compressed G1 point into a 128-byte
// uncompressed format. Format: (X, Y) each as a 64-byte big-endian limb.
func decompressG1To128(sig []byte) ([]byte, error) {
	if len(sig) != 48 {
		return nil, fmt.Errorf("invalid signature length: expected 48, got %d", len(sig))
	}
	p := new(blst.P1Affine).Uncompress(sig)
	if p == nil {
		return nil, fmt.Errorf("failed to uncompress G1 point")
	}
	if !p.SigValidate(true) {
		return nil, fmt.Errorf("invalid G1 signature")
	}
	raw := p.Serialize() // [X(48), Y(48)]
	out := make([]byte, 128)
	copy(out[16:64], raw[0:48])
	copy(out[64+16:128], raw[48:96])
	return out, nil
}

// decompressG2To256 converts a 96-byte compressed G2 point into a 256-byte
// uncompressed format. Format: (X.c0, X.c1, Y.c0, Y.c1) each as a 64-byte
// big-endian limb.
func decompressG2To256(key []byte) ([]byte, error) {
	if len(key) != 96 {
		return nil, fmt.Errorf("invalid G2 key length: expected 96, got %d", len(key))
	}
	p := new(blst.P2Affine).Uncompress(key)
	if p == nil {
		return nil, fmt.Errorf("failed to uncompress G2 point")
	}
	if !p.KeyValidate() {
		return nil, fmt.Errorf("invalid G2 public key")
	}
	raw := p.Serialize() // [X.c1(48), X.c0(48), Y.c1(48), Y.c0(48)] — IETF order
	out := make([]byte, 256)
	copy(out[0*64+16:1*64], raw[48:96])   // X.c0
	copy(out[1*64+16:2*64], raw[0:48])    // X.c1
	copy(out[2*64+16:3*64], raw[144:192]) // Y.c0
	copy(out[3*64+16:4*64], raw[96:144])  // Y.c1
	return out, nil
}

// See: decentralized-api/internal/server/public/bls_handlers.go:17
func (h *Handlers) GetBLSEpoch(ctx echo.Context, id uint64) error {
	res, err := h.chain.BLSQueryClient().EpochBLSData(
		ctx.Request().Context(),
		&blstypes.QueryEpochBLSDataRequest{EpochId: id},
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to query BLS epoch data: "+err.Error())
	}

	var uncompressedG2 []byte
	if len(res.EpochData.GroupPublicKey) == 96 {
		uncompressedG2, _ = decompressG2To256(res.EpochData.GroupPublicKey)
	}

	var uncompressedValSig []byte
	if len(res.EpochData.ValidationSignature) == 48 {
		uncompressedValSig, _ = decompressG1To128(res.EpochData.ValidationSignature)
	}

	epochData, err := protoToRawJSON(&res.EpochData)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to encode BLS epoch data: "+err.Error())
	}

	return ctx.JSON(http.StatusOK, gen.BLSEpochResponse{
		EpochData:                          epochData,
		GroupPublicKeyUncompressed256:      uncompressedG2,
		ValidationSignatureUncompressed128: uncompressedValSig,
	})
}

// See: decentralized-api/internal/server/public/bls_handlers.go:17
func (h *Handlers) GetBLSEpochs(ctx echo.Context, id uint64) error {
	return h.GetBLSEpoch(ctx, id)
}

// Ported from decentralized-api/internal/server/public/bls_handlers.go:51
// Changes:
//   - Not-found detection uses gRPC status code (codes.NotFound) instead of string matching on err.Error().
//   - Not-found response serializes as {} instead of {"signing_request":null} due to omitempty on BLSSignatureResponse.
func (h *Handlers) GetBLSSignature(ctx echo.Context, requestId string) error {
	requestIDBytes, err := hex.DecodeString(requestId)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid request ID format (must be hex-encoded)")
	}

	res, err := h.chain.BLSQueryClient().SigningStatus(
		ctx.Request().Context(),
		&blstypes.QuerySigningStatusRequest{RequestId: requestIDBytes},
	)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return ctx.JSON(http.StatusOK, gen.BLSSignatureResponse{SigningRequest: nil})
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to query BLS signature data: "+err.Error())
	}

	var uncompressedSig *[]byte
	if res != nil && res.SigningRequest.Status == blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED {
		sig := res.SigningRequest.FinalSignature
		if len(sig) == 48 {
			if b, e := decompressG1To128(sig); e == nil {
				uncompressedSig = &b
			}
		}
	}

	sigReq, err := protoToRawJSONPtr(&res.SigningRequest)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to encode BLS signing request: "+err.Error())
	}
	return ctx.JSON(http.StatusOK, gen.BLSSignatureResponse{
		SigningRequest:           sigReq,
		UncompressedSignature128: uncompressedSig,
	})
}
