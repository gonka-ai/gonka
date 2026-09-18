package chain

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

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
// Defined here so dependents (e.g. common/queryapi, devshard/bridge) can reference it without
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
//
// queryConn serves the module query clients and may be a fallback connection;
// conn is always the direct gRPC connection and is what Conn() exposes for
// transaction signing and broadcasting.
type Client struct {
	conn      grpc.ClientConnInterface
	queryConn grpc.ClientConnInterface
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

// DefaultCometRPCPort is the port CometBFT serves JSON-RPC on by default.
const DefaultCometRPCPort = "26657"

// RPCURLFromGRPCURL derives a CometBFT RPC endpoint from a chain gRPC endpoint
// by keeping the host and swapping in the standard RPC port. Every deployment we
// ship co-locates the two, so this lets the query fallback work without a second
// mandatory setting. Returns "" when no host can be extracted.
func RPCURLFromGRPCURL(grpcURL string) string {
	host := strings.TrimSpace(grpcURL)
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+len("://"):]
	}
	// gRPC targets may carry a resolver prefix such as "dns:///node:9090".
	host = strings.Trim(host, "/")
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" {
		return ""
	}
	return "http://" + net.JoinHostPort(host, DefaultCometRPCPort)
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
	conn, err := dialDirect(grpcURL)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, queryConn: conn}, nil
}

// NewWithRPCFallback dials the chain gRPC endpoint and prepares a CometBFT RPC
// connection used for queries whenever gRPC is unreachable. Queries start on
// gRPC, move to RPC on transport failure, and move back once a probe every
// DefaultRPCProbeInterval finds gRPC reachable again — no restart needed.
//
// Conn() still returns the direct gRPC connection. Unordered gateway
// transactions opt into one-shot fallback through UnorderedTxConn.
func NewWithRPCFallback(grpcURL, rpcURL string) (*Client, error) {
	conn, err := dialDirect(grpcURL)
	if err != nil {
		return nil, err
	}
	rpcConn, err := newRPCQueryConn(rpcURL)
	if err != nil {
		return nil, err
	}
	fallback := newFallbackConn(conn, rpcConn, DefaultRPCProbeInterval, time.Now)
	return &Client{conn: conn, queryConn: fallback}, nil
}

// NewWithQueryFallback is NewWithRPCFallback with the endpoint policy every
// service shares: rpcURL may be empty, in which case it is derived from the gRPC
// host, and a client that cannot determine an RPC endpoint at all still runs on
// gRPC alone. Callers pass whatever their own env vars gave them and get the
// same behaviour everywhere.
//
// Degrading rather than failing is deliberate: a fallback meant to keep queries
// working must not be the reason a deployment stops booting.
func NewWithQueryFallback(grpcURL, rpcURL string) (*Client, error) {
	resolved := strings.TrimSpace(rpcURL)
	if resolved == "" {
		resolved = RPCURLFromGRPCURL(grpcURL)
	}
	if resolved == "" {
		slog.Warn("chain: no CometBFT RPC endpoint, queries run on direct gRPC only",
			"grpc_url", grpcURL)
		return New(grpcURL)
	}
	return NewWithRPCFallback(grpcURL, resolved)
}

func dialDirect(grpcURL string) (grpc.ClientConnInterface, error) {
	conn, err := grpc.NewClient(grpcURL, DialOption())
	if err != nil {
		return nil, fmt.Errorf("chain: dial %s: %w", grpcURL, err)
	}
	return observability.NewObservedConn(conn), nil
}

// NewFromConn creates a Client from an existing connection.
// Intended for tests that use in-process gRPC servers.
func NewFromConn(conn grpc.ClientConnInterface) *Client {
	return &Client{conn: conn, queryConn: conn}
}

// Conn returns the direct gRPC connection for ordered transactions and callers
// that must not use the query fallback.
func (c *Client) Conn() grpc.ClientConnInterface { return c.conn }

// QueryConn returns the connection queries run on. It equals Conn() unless the
// client was built with NewWithRPCFallback. Use it for query clients this
// package does not expose directly.
//
// Queries only. When this is a fallback conn, broadcasting a transaction
// through it is rejected rather than silently retried across transports. Use
// Conn() for transactions either way.
func (c *Client) QueryConn() grpc.ClientConnInterface { return c.queryConn }

// UnorderedTxConn returns a connection for gateway unordered transactions.
// Read operations use QueryConn's gRPC-to-RPC fallback. BroadcastTx uses the
// transport selected by those reads exactly once, preventing cross-transport
// resubmission after an ambiguous broadcast error.
func (c *Client) UnorderedTxConn() grpc.ClientConnInterface {
	if fallback, ok := c.queryConn.(*fallbackConn); ok {
		return &unorderedTxConn{fallback: fallback}
	}
	return c.conn
}

// InferenceQueryClient returns a query client for the inference module.
func (c *Client) InferenceQueryClient() InferenceClient {
	return inferencetypes.NewQueryClient(c.queryConn)
}

// BLSQueryClient returns a query client for the BLS module.
func (c *Client) BLSQueryClient() blstypes.QueryClient {
	return blstypes.NewQueryClient(c.queryConn)
}

// RestrictionsQueryClient returns a query client for the restrictions module.
func (c *Client) RestrictionsQueryClient() restrictionstypes.QueryClient {
	return restrictionstypes.NewQueryClient(c.queryConn)
}

// CometServiceClient returns a client for CometBFT node services
// (node info, block queries, ABCI queries).
func (c *Client) CometServiceClient() cmtservice.ServiceClient {
	return cmtservice.NewServiceClient(c.queryConn)
}
