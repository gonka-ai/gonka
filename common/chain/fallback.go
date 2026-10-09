package chain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"common/observability"
)

// DefaultRPCProbeInterval is how long queries stay on CometBFT RPC before a
// single request is allowed to probe direct gRPC again.
const DefaultRPCProbeInterval = 30 * time.Minute

// broadcastTxMethod is the one method on the tx service that is not a query.
// QueryConn rejects it because retrying a failed broadcast on another transport
// can submit twice. UnorderedTxConn permits it through a one-shot wrapper.
const broadcastTxMethod = "/cosmos.tx.v1beta1.Service/BroadcastTx"

// fallbackConn routes unary query invocations over direct chain gRPC while that
// endpoint is reachable, and over CometBFT RPC/ABCI while it is not.
//
// The switch is driven entirely by observed transport failures, so a node that
// enables gRPC later is picked up without restarting anything: after
// probeInterval on RPC, exactly one request is routed over gRPC, and success
// makes gRPC primary again.
type fallbackConn struct {
	direct grpc.ClientConnInterface
	rpc    grpc.ClientConnInterface

	probeInterval time.Duration
	now           func() time.Time

	mu          sync.Mutex
	usingRPC    bool
	probing     bool
	nextProbeAt time.Time
}

// unorderedTxConn shares fallbackConn's query routing, but permits unordered
// transaction broadcasts. A broadcast uses the already-selected transport
// exactly once; it never retries across transports after an ambiguous error.
type unorderedTxConn struct {
	fallback *fallbackConn
}

func newFallbackConn(direct, rpc grpc.ClientConnInterface, probeInterval time.Duration, now func() time.Time) *fallbackConn {
	// Publish the starting transport so the gauge exists from process start.
	// Without it "no comet-rpc series" is ambiguous between healthy and
	// never-reported.
	observability.Chain.SetQueryTransport(observability.TransportGRPC)
	return &fallbackConn{
		direct:        direct,
		rpc:           rpc,
		probeInterval: probeInterval,
		now:           now,
	}
}

// route decides where the next invocation goes. When it returns probe=true the
// caller owns the single in-flight gRPC probe and must release it via
// probeSucceeded or probeFailed.
func (c *fallbackConn) route() (useRPC, probe bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.usingRPC {
		return false, false
	}
	if !c.probing && !c.now().Before(c.nextProbeAt) {
		c.probing = true
		return false, true
	}
	return true, false
}

// enterRPC switches queries to RPC and reports whether this call caused the
// transition, so concurrent failures log once.
func (c *fallbackConn) enterRPC() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.usingRPC {
		return false
	}
	c.usingRPC = true
	c.nextProbeAt = c.now().Add(c.probeInterval)
	return true
}

func (c *fallbackConn) probeSucceeded() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.usingRPC = false
	c.probing = false
	c.nextProbeAt = time.Time{}
}

func (c *fallbackConn) probeFailed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.probing = false
	c.nextProbeAt = c.now().Add(c.probeInterval)
}

// probeNoVerdict releases the probe as if it had never run: the attempt proved
// nothing either way, so it must neither change transports nor push back a
// probe window that has already come due.
func (c *fallbackConn) probeNoVerdict() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.probing = false
}

func (c *fallbackConn) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	if method == broadcastTxMethod {
		return status.Errorf(codes.InvalidArgument,
			"chain: %s must not run on the query fallback; broadcast transactions on Client.Conn()", method)
	}

	useRPC, probe := c.route()
	if useRPC {
		return c.rpc.Invoke(ctx, method, args, reply, opts...)
	}

	err := c.direct.Invoke(ctx, method, args, reply, opts...)

	if probe {
		switch classifyProbe(ctx, err) {
		case grpcAnswered:
			c.probeSucceeded()
			observability.Chain.SetQueryTransport(observability.TransportGRPC)
			slog.Info("chain: direct gRPC reachable again, queries back on gRPC", "method", method)
			return err

		case noVerdict:
			// The caller went away or ran out of time, so this attempt says
			// nothing about gRPC. Reading it as reachable would move every
			// query back onto an endpoint that was never shown to answer. The
			// caller is already gone, so an RPC retry would only fail too.
			c.probeNoVerdict()
			slog.Debug("chain: gRPC probe ended without a verdict, staying on CometBFT RPC",
				"method", method, "error", err)
			return err

		default: // grpcDown
			c.probeFailed()
			// One line per probe interval is the heartbeat that keeps a
			// long-running fallback visible: without it the only trace of a
			// week on RPC is a single warning from the day it started.
			slog.Warn("chain: direct gRPC still unreachable, queries staying on CometBFT RPC",
				"method", method, "error", err, "retry_after", c.probeInterval)
			return c.retryOnRPC(ctx, method, err, args, reply, opts)
		}
	}

	if !isTransportDown(err) {
		return err
	}
	if c.enterRPC() {
		observability.Chain.SetQueryTransport(observability.TransportCometRPC)
		slog.Warn("chain: direct gRPC unreachable, queries falling back to CometBFT RPC",
			"method", method, "error", err, "retry_after", c.probeInterval)
	}
	return c.retryOnRPC(ctx, method, err, args, reply, opts)
}

// retryOnRPC serves a request that direct gRPC could not, keeping the original
// transport error attached. Without it a node that is unreachable on both ports
// reports only the RPC-side failure, which hides why the fallback engaged.
func (c *fallbackConn) retryOnRPC(
	ctx context.Context,
	method string,
	grpcErr error,
	args, reply any,
	opts []grpc.CallOption,
) error {
	rpcErr := c.rpc.Invoke(ctx, method, args, reply, opts...)
	if rpcErr == nil {
		return nil
	}
	return fmt.Errorf("chain: %s failed on gRPC (%w) and on CometBFT RPC: %w", method, grpcErr, rpcErr)
}

// probeVerdict is what a probe attempt proved about the direct gRPC endpoint.
type probeVerdict int

const (
	// grpcAnswered means the endpoint replied, even if the reply was an
	// application error.
	grpcAnswered probeVerdict = iota
	// grpcDown means the transport itself failed.
	grpcDown
	// noVerdict means the attempt ended before either could be established,
	// because the caller canceled it or its deadline expired.
	noVerdict
)

func classifyProbe(ctx context.Context, err error) probeVerdict {
	if isTransportDown(err) {
		return grpcDown
	}
	if err == nil {
		return grpcAnswered
	}
	switch status.Code(err) {
	case codes.Canceled, codes.DeadlineExceeded:
		return noVerdict
	}
	// A wrapper may surface a bare context error instead of a gRPC status, and
	// a context that is already done means the attempt was cut short whatever
	// the error says.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return noVerdict
	}
	return grpcAnswered
}

// NewStream always uses direct gRPC: the RPC/ABCI transport cannot serve
// streams, so there is nothing to fall back to.
func (c *fallbackConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return c.direct.NewStream(ctx, desc, method, opts...)
}

func (c *unorderedTxConn) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	if method != broadcastTxMethod {
		return c.fallback.Invoke(ctx, method, args, reply, opts...)
	}
	c.fallback.mu.Lock()
	useRPC := c.fallback.usingRPC
	c.fallback.mu.Unlock()
	if useRPC {
		return c.fallback.rpc.Invoke(ctx, method, args, reply, opts...)
	}
	return c.fallback.direct.Invoke(ctx, method, args, reply, opts...)
}

func (c *unorderedTxConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return c.fallback.NewStream(ctx, desc, method, opts...)
}

// isTransportDown reports whether err means the chain gRPC endpoint is
// unreachable, as opposed to the query itself being rejected.
func isTransportDown(err error) bool {
	if err == nil {
		return false
	}
	if st, ok := status.FromError(err); ok {
		return st.Code() == codes.Unavailable
	}
	return errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNREFUSED)
}

// newRPCQueryConn builds a Cosmos SDK client.Context that serves generated query
// clients over CometBFT RPC. With no GRPCClient set, Context.Invoke wraps each
// query as an ABCI query against the node's gRPC query router. It is wrapped in
// the same span recorder as the direct conn, tagged with its own transport, so
// switching to the fallback changes which transport spans report rather than
// making them stop.
//
// Known limitation: module queries (inference, BLS, restrictions) are registered
// on that router by every node, but the CometBFT service
// (cosmos.base.tendermint.v1beta1) is registered only when app.toml sets
// grpc.enable or api.enable, because the SDK gates RegisterTendermintService on
// them. grpc.enable defaults to true and no deployment here turns it off, so in
// practice those queries resolve; a node with both disabled would answer them
// with Unimplemented while module queries kept working. Serving them from
// CometBFT's own /status, /block and /validators endpoints would close that gap
// if such a node ever needs supporting.
func newRPCQueryConn(rpcURL string) (grpc.ClientConnInterface, error) {
	if strings.TrimSpace(rpcURL) == "" {
		return nil, fmt.Errorf("chain: comet rpc url is required for query fallback")
	}
	node, err := client.NewClientFromNode(rpcURL)
	if err != nil {
		return nil, fmt.Errorf("chain: comet rpc client %s: %w", rpcURL, err)
	}

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	authtypes.RegisterInterfaces(registry)
	inferencetypes.RegisterInterfaces(registry)
	blstypes.RegisterInterfaces(registry)
	restrictionstypes.RegisterInterfaces(registry)

	rpcCtx := client.Context{}.
		WithClient(node).
		WithNodeURI(rpcURL).
		WithCodec(codec.NewProtoCodec(registry)).
		WithInterfaceRegistry(registry)

	return observability.NewObservedConnWithTransport(rpcCtx, observability.TransportCometRPC), nil
}
