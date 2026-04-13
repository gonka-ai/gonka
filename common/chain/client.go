package chain

import (
	"context"
	"fmt"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// InferenceClient is the narrow subset of inferencetypes.QueryClient used by this module.
// Defined here so dependents (e.g. queryapi/impl, devshard/bridge) can reference it without
// importing the full generated proto package.
type InferenceClient interface {
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
}

// Client provides blockchain queries via gRPC.
// Consumers that need mocking define their own narrow interfaces
// with only the methods they call.
type Client struct {
	conn grpc.ClientConnInterface
}

// New dials the chain gRPC endpoint eagerly and returns a Client.
// cfg is assumed valid — config.Load guarantees this.
func New(GRPCUrl string) (*Client, error) {
	conn, err := grpc.NewClient(
		GRPCUrl,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("chain: dial %s: %w", GRPCUrl, err)
	}
	return &Client{conn: conn}, nil
}

// NewFromConn creates a Client from an existing connection.
// Intended for tests that use in-process gRPC servers.
func NewFromConn(conn grpc.ClientConnInterface) (*Client, error) {
	return &Client{conn: conn}, nil
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
