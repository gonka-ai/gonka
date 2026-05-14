// Package bridge implements devshard/bridge.MainnetBridge on top of the
// testenv mock-chain gRPC service.
//
// It is imported only by cmd/devshardd-testenv; production devshardd must
// not depend on this package. The bridge lives in devshard/testenv/bridge
// rather than devshard/bridge to keep that dependency rule obvious (see
// §8.4 reusability sanity checks).
//
// Query methods (GetEscrow, GetHostInfo, VerifyWarmKey) round-trip to
// mock-chain. Notification and action methods (OnEscrowCreated,
// OnSettlementProposed, OnSettlementFinalized, SubmitDisputeState) match
// the production RESTBridge shape and return ErrNotImplemented — the
// testenv has no chain-event stream and does not drive disputes.
package bridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	devbridge "devshard/bridge"
	"devshard/testenv/proto/mockchainpb"
)

// warmKeyMsgType mirrors the constant in devshard/bridge/rest.go. Kept
// here as a private copy so the testenv bridge does not reach into an
// unexported symbol in the prod package.
const warmKeyMsgType = "/inference.inference.MsgStartInference"

// defaultCallTimeout bounds every unary call made by the bridge. Mock-chain
// responses are in-memory and normally return in <1 ms; the timeout only
// fires when the container is unreachable.
const defaultCallTimeout = 5 * time.Second

// GRPCBridge is a testenv-only MainnetBridge backed by mock-chain gRPC.
//
// Concurrency: all fields except warmCache are immutable after construction.
// warmCache is a sync.Map.
type GRPCBridge struct {
	conn    *grpc.ClientConn
	client  mockchainpb.MockChainClient
	timeout time.Duration

	warmCache sync.Map // warmCacheKey -> bool
}

type warmCacheKey struct {
	validator string
	warm      string
}

// Option configures a GRPCBridge. Dial-level options are not exposed; the
// testenv always uses insecure credentials against a container on the
// same docker network.
type Option func(*GRPCBridge)

// WithTimeout overrides the per-call timeout.
func WithTimeout(d time.Duration) Option {
	return func(b *GRPCBridge) {
		if d > 0 {
			b.timeout = d
		}
	}
}

// NewGRPCBridge dials mock-chain at target (e.g. "mock-chain:9090") and
// returns a bridge that satisfies devshard/bridge.MainnetBridge. The
// returned bridge owns the underlying grpc.ClientConn and must be Close()d
// when the devshardd process is shutting down.
//
// ctx is only used to bound the (non-blocking) dial resolution; the
// connection itself is kept alive until Close.
func NewGRPCBridge(ctx context.Context, target string, opts ...Option) (*GRPCBridge, error) {
	if target == "" {
		return nil, errors.New("testenv/bridge: empty mock-chain target")
	}
	if ctx == nil {
		return nil, errors.New("testenv/bridge: nil context")
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("testenv/bridge: dial %s: %w", target, err)
	}
	b := &GRPCBridge{
		conn:    conn,
		client:  mockchainpb.NewMockChainClient(conn),
		timeout: defaultCallTimeout,
	}
	for _, o := range opts {
		o(b)
	}
	return b, nil
}

// NewGRPCBridgeWithClient wraps a pre-constructed MockChainClient. Used
// by tests that dial mock-chain over bufconn; the caller owns the
// underlying connection.
func NewGRPCBridgeWithClient(client mockchainpb.MockChainClient, opts ...Option) *GRPCBridge {
	if client == nil {
		panic("testenv/bridge: nil MockChainClient")
	}
	b := &GRPCBridge{
		client:  client,
		timeout: defaultCallTimeout,
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Close releases the underlying grpc.ClientConn. No-op when the bridge
// was built via NewGRPCBridgeWithClient (the caller owns the connection).
func (b *GRPCBridge) Close() error {
	if b.conn == nil {
		return nil
	}
	return b.conn.Close()
}

// MockChainClient exposes the underlying client for tooling (devshardctl)
// that needs to drive CreateEscrow / SettleEscrow directly. devshardd
// itself never uses these entry points.
func (b *GRPCBridge) MockChainClient() mockchainpb.MockChainClient {
	return b.client
}

func (b *GRPCBridge) callCtx(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if b.timeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, b.timeout)
}

// ── Queries ───────────────────────────────────────────────────────────────────

// GetEscrow mirrors the REST bridge: returns ErrEscrowNotFound on
// NotFound, propagates every other error verbatim.
func (b *GRPCBridge) GetEscrow(escrowID string) (*devbridge.EscrowInfo, error) {
	ctx, cancel := b.callCtx(context.Background())
	defer cancel()

	resp, err := b.client.GetDevshardEscrow(ctx, &mockchainpb.GetDevshardEscrowRequest{EscrowId: escrowID})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, devbridge.ErrEscrowNotFound
		}
		return nil, fmt.Errorf("testenv/bridge: GetDevshardEscrow %s: %w", escrowID, err)
	}

	return &devbridge.EscrowInfo{
		EscrowID:       resp.GetEscrowId(),
		Amount:         resp.GetAmount(),
		CreatorAddress: resp.GetCreatorAddress(),
		AppHash:        append([]byte(nil), resp.GetAppHash()...),
		Slots:          append([]string(nil), resp.GetSlots()...),
		TokenPrice:     resp.GetTokenPrice(),
	}, nil
}

// GetHostInfo returns ErrParticipantNotFound on NotFound, any other gRPC
// error is wrapped and propagated.
func (b *GRPCBridge) GetHostInfo(address string) (*devbridge.HostInfo, error) {
	ctx, cancel := b.callCtx(context.Background())
	defer cancel()

	resp, err := b.client.GetParticipant(ctx, &mockchainpb.GetParticipantRequest{Address: address})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, devbridge.ErrParticipantNotFound
		}
		return nil, fmt.Errorf("testenv/bridge: GetParticipant %s: %w", address, err)
	}
	return &devbridge.HostInfo{
		Address: resp.GetAddress(),
		URL:     resp.GetUrl(),
	}, nil
}

// GetValidationThreshold is not backed by mock-chain; callers that need a
// live epoch snapshot should use the production REST bridge instead.
func (b *GRPCBridge) GetValidationThreshold(uint64, string) (*devbridge.Decimal, error) {
	return nil, devbridge.ErrNotImplemented
}

// VerifyWarmKey returns true iff warmAddress is a grantee authorised to
// sign the MsgStartInference message type for validatorAddress. Matches
// the prod bridge's semantics and cache behaviour; the testenv
// mock-chain returns every participant address, so any configured host
// key is a valid grantee for any validator.
func (b *GRPCBridge) VerifyWarmKey(warmAddress, validatorAddress string) (bool, error) {
	key := warmCacheKey{validator: validatorAddress, warm: warmAddress}
	if cached, ok := b.warmCache.Load(key); ok {
		return cached.(bool), nil
	}

	ctx, cancel := b.callCtx(context.Background())
	defer cancel()

	resp, err := b.client.GetGrantees(ctx, &mockchainpb.GetGranteesRequest{
		ValidatorAddress: validatorAddress,
		MessageType:      warmKeyMsgType,
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			b.warmCache.Store(key, false)
			return false, nil
		}
		return false, fmt.Errorf("testenv/bridge: GetGrantees %s: %w", validatorAddress, err)
	}

	found := false
	for _, addr := range resp.GetAddresses() {
		if addr == warmAddress {
			found = true
			break
		}
	}
	b.warmCache.Store(key, found)
	return found, nil
}

// ── Notifications and actions (stubs) ─────────────────────────────────────────
//
// The mock-chain does not emit chain events and does not implement a
// dispute pipeline. Match the prod RESTBridge: return ErrNotImplemented
// so an accidental invocation from HostManager is loud rather than
// silently swallowed.

func (b *GRPCBridge) OnEscrowCreated(_ devbridge.EscrowInfo) error {
	return devbridge.ErrNotImplemented
}

func (b *GRPCBridge) OnSettlementProposed(_ string, _ []byte, _ uint64) error {
	return devbridge.ErrNotImplemented
}

func (b *GRPCBridge) OnSettlementFinalized(_ string) error {
	return devbridge.ErrNotImplemented
}

func (b *GRPCBridge) SubmitDisputeState(_ string, _ []byte, _ uint64, _ map[uint32][]byte) error {
	return devbridge.ErrNotImplemented
}

// Compile-time contract check — fails the build if the testenv bridge
// ever drifts from devshard/bridge.MainnetBridge.
var _ devbridge.MainnetBridge = (*GRPCBridge)(nil)
