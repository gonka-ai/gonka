package middleware_test

import (
	"errors"
	"net/http"
	"testing"

	"decentralized-api/internal/server/middleware"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestExtractError(t *testing.T) {
	baseErr := errors.New("inference server is not running")

	// 1. Generic error
	status, msg := middleware.ExtractError(baseErr)
	require.Equal(t, http.StatusInternalServerError, status)
	require.Equal(t, baseErr.Error(), msg)

	// 2. echo.HTTPError preserving original payload and status code
	httpErr := echo.NewHTTPError(http.StatusBadRequest, baseErr)

	status, msg = middleware.ExtractError(httpErr)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, baseErr, msg)

	// 3. Model not supported error returns 400
	modelErr := echo.NewHTTPError(http.StatusBadRequest, "Model not supported")

	status, msg = middleware.ExtractError(modelErr)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "Model not supported", msg)
}

func TestExtractError_GrpcErrorFallsBackTo500(t *testing.T) {
	// Raw gRPC errors without proper mapping produce 500.
	// This is the behaviour that issue #351 reported: callers must
	// convert gRPC errors into echo.HTTPError before returning them.
	grpcErr := grpcstatus.Error(codes.Internal, "epoch group data not found")

	st, msg := middleware.ExtractError(grpcErr)
	require.Equal(t, http.StatusInternalServerError, st)
	require.Contains(t, msg, "epoch group data not found")
}
