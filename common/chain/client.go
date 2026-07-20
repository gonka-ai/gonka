package chain

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"strings"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"common/observability"
)

// EnvChainGRPCTLS enables TLS for Dial / New when set to a truthy value
// ("1", "true", "yes"). Default is insecure (plaintext), matching co-located
// node deployments.
const EnvChainGRPCTLS = "CHAIN_GRPC_TLS"

// InferenceClient is the narrow subset of inferencetypes.QueryClient used by this module.
// Defined here so dependents (e.g. edge-api/queryapi, devshard/bridge) can reference it without
// importing the full generated proto package.
type InferenceClient interface {
	Params(context.Context, *inferencetypes.QueryParamsRequest, ...grpc.CallOption) (*inferencetypes.QueryParamsResponse, error)
	EpochInfo(context.Context, *inferencetypes.QueryEpochInfoRequest, ...grpc.CallOption) (*inferencetypes.QueryEpochInfoResponse, error)
	GetCurrentEpoch(ctx context.Context, in *inferencetypes.QueryGetCurrentEpochRequest, opts ...grpc.CallOption) (*inferencetypes.QueryGetCurrentEpochResponse, error)
	ParticipantsWithBalances(context.Context, *inferencetypes.QueryParticipantsWithBalancesRequest, ...grpc.CallOption) (*inferencetypes.QueryParticipantsWithBalancesResponse, error)
	AccountByAddress(context.Context, *inferencetypes.QueryAccountByAddressRequest, ...grpc.CallOption) (*inferencetypes.QueryAccountByAddressResponse, error)
	Participant(context.Context, *inferencetypes.QueryGetParticipantRequest, ...grpc.CallOption) (*inferencetypes.QueryGetParticipantResponse, error)
	DevshardEscrow(context.Context, *inferencetypes.QueryGetDevshardEscrowRequest, ...grpc.CallOption) (*inferencetypes.QueryGetDevshardEscrowResponse, error)
	GranteesByMessageType(context.Context, *inferencetypes.QueryGranteesByMessageTypeRequest, ...grpc.CallOption) (*inferencetypes.QueryGranteesByMessageTypeResponse, error)
	ExcludedParticipants(context.Context, *inferencetypes.QueryExcludedParticipantsRequest, ...grpc.CallOption) (*inferencetypes.QueryExcludedParticipantsResponse, error)
	PocBatchesForStage(context.Context, *inferencetypes.QueryPocBatchesForStageRequest, ...grpc.CallOption) (*inferencetypes.QueryPocBatchesForStageResponse, error)
	BridgeAddressesByChain(context.Context, *inferencetypes.QueryBridgeAddressesByChainRequest, ...grpc.CallOption) (*inferencetypes.QueryBridgeAddressesByChainResponse, error)
	CurrentEpochGroupData(context.Context, *inferencetypes.QueryCurrentEpochGroupDataRequest, ...grpc.CallOption) (*inferencetypes.QueryCurrentEpochGroupDataResponse, error)
	EpochGroupData(context.Context, *inferencetypes.QueryGetEpochGroupDataRequest, ...grpc.CallOption) (*inferencetypes.QueryGetEpochGroupDataResponse, error)
	ModelsAll(context.Context, *inferencetypes.QueryModelsAllRequest, ...grpc.CallOption) (*inferencetypes.QueryModelsAllResponse, error)
	GetAllModelPerTokenPrices(context.Context, *inferencetypes.QueryGetAllModelPerTokenPricesRequest, ...grpc.CallOption) (*inferencetypes.QueryGetAllModelPerTokenPricesResponse, error)
	GetAllModelCapacities(context.Context, *inferencetypes.QueryGetAllModelCapacitiesRequest, ...grpc.CallOption) (*inferencetypes.QueryGetAllModelCapacitiesResponse, error)
	PreservedNodesSnapshot(context.Context, *inferencetypes.QueryPreservedNodesSnapshotRequest, ...grpc.CallOption) (*inferencetypes.QueryPreservedNodesSnapshotResponse, error)
}

// Client provides blockchain queries via gRPC.
// Consumers that need mocking define their own narrow interfaces
// with only the methods they call.
type Client struct {
	conn grpc.ClientConnInterface
}

// TLSEnabled reports whether CHAIN_GRPC_TLS requests TLS for chain gRPC dials.
func TLSEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvChainGRPCTLS))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// DialOption returns the transport credential DialOption for chain gRPC.
// Default is insecure; set CHAIN_GRPC_TLS=true for system-root TLS.
func DialOption() grpc.DialOption {
	if TLSEnabled() {
		return grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS12,
		}))
	}
	return grpc.WithTransportCredentials(insecure.NewCredentials())
}

// New dials the chain gRPC endpoint eagerly and returns a Client.
// cfg is assumed valid — config.Load guarantees this.
// Transport is plaintext unless CHAIN_GRPC_TLS is truthy.
func New(grpcURL string) (*Client, error) {
	conn, err := grpc.NewClient(grpcURL, DialOption())
	if err != nil {
		return nil, fmt.Errorf("chain: dial %s: %w", grpcURL, err)
	}
	return &Client{conn: observability.NewObservedConn(conn)}, nil
}

// NewFromConn creates a Client from an existing connection.
// Intended for tests that use in-process gRPC servers.
func NewFromConn(conn grpc.ClientConnInterface) *Client {
	return &Client{conn: conn}
}

// Conn returns the underlying gRPC connection.
func (c *Client) Conn() grpc.ClientConnInterface { return c.conn }

// InferenceQueryClient returns a query client for the inference module.
func (c *Client) InferenceQueryClient() InferenceClient {
	return inferencetypes.NewQueryClient(c.conn)
}

// BLSQueryClient returns a query client for the BLS module.
func (c *Client) BLSQueryClient() blstypes.QueryClient {
	return blstypes.NewQueryClient(c.conn)
}

// RestrictionsQueryClient returns a query client for the restrictions module.
func (c *Client) RestrictionsQueryClient() restrictionstypes.QueryClient {
	return restrictionstypes.NewQueryClient(c.conn)
}

// CometServiceClient returns a client for CometBFT node services
// (node info, block queries, ABCI queries).
func (c *Client) CometServiceClient() cmtservice.ServiceClient {
	return cmtservice.NewServiceClient(c.conn)
}
