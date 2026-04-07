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
	"common/queryapi/gen"
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

// ptr returns a pointer to v. Used for optional fields in generated structs.
func ptr[T any](v T) *T { return &v }

// balanceNGonka extracts the ngonka balance from a coin list.
func balanceNGonka(coins []sdktypes.Coin) int64 {
	for _, c := range coins {
		if c.Denom == "ngonka" {
			return c.Amount.Int64()
		}
	}
	return 0
}

// -- stub handlers (not yet implemented) --

func (h *Handlers) GetStatus(ctx echo.Context) error {
	return ctx.JSON(http.StatusNotImplemented, gen.StatusResponse{})
}

func (h *Handlers) GetVersions(ctx echo.Context) error {
	return ctx.JSON(http.StatusNotImplemented, gen.VersionsResponse{})
}

func (h *Handlers) GetBridgeAddresses(ctx echo.Context, params gen.GetBridgeAddressesParams) error {
	return ctx.JSON(http.StatusNotImplemented, gen.BridgeAddressesResponse{})
}

func (h *Handlers) GetPoCBatches(ctx echo.Context, epoch int) error {
	return ctx.JSON(http.StatusNotImplemented, nil)
}

func (h *Handlers) PostVerifyProof(ctx echo.Context) error {
	return ctx.JSON(http.StatusNotImplemented, nil)
}

func (h *Handlers) PostVerifyBlock(ctx echo.Context) error {
	return ctx.JSON(http.StatusNotImplemented, nil)
}

func (h *Handlers) DebugPubKeyToAddr(ctx echo.Context, pubkey string) error {
	return ctx.JSON(http.StatusNotImplemented, nil)
}

func (h *Handlers) DebugVerifyBlockSignatures(ctx echo.Context, height int) error {
	return ctx.JSON(http.StatusNotImplemented, nil)
}
