package queryapi

import (
	"net/http"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	sdktypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/labstack/echo/v4"
	blstypes "github.com/productscience/inference/x/bls/types"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"common/chain"
	"edge-api/queryapi/gen"
)

// ChainClient is the interface queryapi handlers require from chain.Client.
// *chain.Client satisfies this automatically.
type ChainClient interface {
	InferenceQueryClient() chain.InferenceClient
	BLSQueryClient() blstypes.QueryClient
	RestrictionsQueryClient() restrictionstypes.QueryClient
	CometServiceClient() cmtservice.ServiceClient
}

// Handlers implements gen.ServerInterface using a chain gRPC client.
type Handlers struct {
	chain ChainClient
}

// NewHandlers creates a Handlers. c must not be nil.
func NewHandlers(c ChainClient) *Handlers {
	return &Handlers{chain: c}
}

var _ gen.ServerInterface = (*Handlers)(nil)

// grpcErrorToHTTP maps gRPC status codes to HTTP errors.
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

// balanceNGonka extracts the ngonka balance from a coin list.
func balanceNGonka(coins []sdktypes.Coin) int64 {
	for _, c := range coins {
		if c.Denom == "ngonka" {
			return c.Amount.Int64()
		}
	}
	return 0
}
