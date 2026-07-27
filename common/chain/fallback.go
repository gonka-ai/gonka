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
)

// DefaultRPCProbeInterval is how long queries stay on CometBFT RPC before a
// single request is allowed to probe direct gRPC again.
const DefaultRPCProbeInterval = 30 * time.Minute

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

func newFallbackConn(direct, rpc grpc.ClientConnInterface, probeInterval time.Duration, now func() time.Time) *fallbackConn {
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

func (c *fallbackConn) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	useRPC, probe := c.route()
	if useRPC {
		return c.rpc.Invoke(ctx, method, args, reply, opts...)
	}

	err := c.direct.Invoke(ctx, method, args, reply, opts...)
	if !isTransportDown(err) {
		// Any other outcome, including application errors, proves gRPC is
		// reachable — keep it primary.
		if probe {
			c.probeSucceeded()
			slog.Info("chain: direct gRPC reachable again, queries back on gRPC", "method", method)
		}
		return err
	}

	if probe {
		c.probeFailed()
	} else if c.enterRPC() {
		slog.Warn("chain: direct gRPC unreachable, queries falling back to CometBFT RPC",
			"method", method, "error", err, "retry_after", c.probeInterval)
	}
	return c.rpc.Invoke(ctx, method, args, reply, opts...)
}

// NewStream always uses direct gRPC: the RPC/ABCI transport cannot serve
// streams, so there is nothing to fall back to.
func (c *fallbackConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return c.direct.NewStream(ctx, desc, method, opts...)
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
// query as an ABCI query against the node's gRPC query router.
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

	return client.Context{}.
		WithClient(node).
		WithNodeURI(rpcURL).
		WithCodec(codec.NewProtoCodec(registry)).
		WithInterfaceRegistry(registry), nil
}
