package transport

import (
	"context"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"devshard/gossip"
	"devshard/heightsync"
	"devshard/host"
	"devshard/signing"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/types"
)

// RPCClient is the dual-stack peer client. Named endpoints go over Connect
// via PeerConn; everything else delegates to HTTPClient. Default config
// (empty EndpointSet) never constructs this type.
type RPCClient struct {
	*HTTPClient
	conn      *PeerConn
	endpoints EndpointSet
	session   rpcpbconnect.SessionServiceClient
	gossip    rpcpbconnect.GossipServiceClient
	// closeOnce is a pointer so WithoutAdmission can copy RPCClient without
	// copying a sync.Once (finding 20 / go vet copylocks).
	closeOnce *sync.Once
	// ownsConn is true only on the SelectTransport / NewRPCClient value that
	// holds the registry ref. Finalize clones must not Release.
	ownsConn bool
}

// NewRPCClient wraps http with a shared PeerConn. conn may be nil only in
// tests that never call an RPC-selected method.
func NewRPCClient(httpClient *HTTPClient, conn *PeerConn, endpoints EndpointSet) *RPCClient {
	c := &RPCClient{
		HTTPClient: httpClient,
		conn:       conn,
		endpoints:  endpoints,
		closeOnce:  new(sync.Once),
		ownsConn:   conn != nil,
	}
	if conn != nil && httpClient != nil {
		base := conn.cfg.connectBase(httpClient.escrowID)
		opts := connectClientOptions(conn.cfg.ReadMaxBytes)
		c.session = rpcpbconnect.NewSessionServiceClient(conn.http, base, opts...)
		c.gossip = rpcpbconnect.NewGossipServiceClient(conn.http, base, opts...)
		// Chat and validation GetPayload must use DefaultMaxBodySize (10 MiB),
		// not DefaultRPCReadMaxBytes (finding 27).
	}
	return c
}

func (c *RPCClient) Uses(name string) bool {
	return c != nil && c.endpoints.Has(name)
}

func (c *RPCClient) Close() {
	if c == nil || c.closeOnce == nil {
		return
	}
	c.closeOnce.Do(func() {
		if c.ownsConn && c.conn != nil {
			c.conn.Release()
		}
	})
}

func (c *RPCClient) WithoutAdmission() any {
	if c == nil {
		return (*RPCClient)(nil)
	}
	httpAny := c.HTTPClient.WithoutAdmission()
	httpClient, _ := httpAny.(*HTTPClient)
	out := *c
	out.HTTPClient = httpClient
	out.ownsConn = false
	out.closeOnce = new(sync.Once)
	return &out
}

func tokenRequest[T any](c *RPCClient, msg *T) (*connect.Request[T], error) {
	if c.conn == nil || !c.conn.Ready() {
		return nil, ErrPeerNotReady
	}
	tok := c.conn.LiveToken()
	if len(tok) == 0 {
		return nil, ErrPeerNotReady
	}
	req := connect.NewRequest(msg)
	SetSessionHeader(req.Header(), tok)
	return req, nil
}

func rpcRetry(ctx context.Context, fn func() error) error {
	deadline := nonInferenceRetryDeadline(ctx)
	delay := nonInferenceRetryInitial
	var last error
	unauthRetries := 0
	for {
		err := fn()
		if err == nil {
			return nil
		}
		if !isRetryableRPC(err, &unauthRetries) {
			if last != nil && isContextFinished(err) {
				return last
			}
			return err
		}
		last = err
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return err
		}
		sleep := delay
		if sleep > remaining {
			sleep = remaining
		}
		if err := sleepContext(ctx, sleep); err != nil {
			return last
		}
		if delay < nonInferenceRetryBudget {
			delay *= 2
		}
	}
}

const maxUnauthenticatedRPCRetries = 1

// isRetryableRPC is the Connect retry policy. Unavailable / ResourceExhausted
// use the shared 5 s budget. Unauthenticated is one extra attempt for a token
// rotation race (finding 10); a stable unauthenticated session fails fast.
func isRetryableRPC(err error, unauthRetries *int) bool {
	if IsRetryableNonInference(err) {
		return true
	}
	if connect.CodeOf(err) == connect.CodeUnauthenticated && *unauthRetries < maxUnauthenticatedRPCRetries {
		*unauthRetries++
		return true
	}
	return false
}

func connectClientOptions(maxBytes int) []connect.ClientOption {
	if maxBytes <= 0 {
		maxBytes = DefaultRPCReadMaxBytes
	}
	return []connect.ClientOption{connect.WithReadMaxBytes(maxBytes)}
}

func (c *RPCClient) GetSignatures(ctx context.Context, nonce uint64) (map[uint32][]byte, error) {
	if !c.Uses(EndpointSignatures) {
		return c.HTTPClient.GetSignatures(ctx, nonce)
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.QueryTimeout)
	defer cancel()
	var out map[uint32][]byte
	err := rpcRetry(ctx, func() error {
		req, err := tokenRequest(c, &rpcpb.GetSignaturesRequest{Nonce: nonce})
		if err != nil {
			return err
		}
		resp, err := c.session.GetSignatures(ctx, req)
		if err != nil {
			return err
		}
		out = resp.Msg.GetSignatures()
		if out == nil {
			out = map[uint32][]byte{}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("get signatures: %w", err)
	}
	return out, nil
}

func (c *RPCClient) GetDiffs(ctx context.Context, from, to uint64) ([]types.Diff, error) {
	if !c.Uses(EndpointDiffs) {
		return c.HTTPClient.GetDiffs(ctx, from, to)
	}
	return nil, fmt.Errorf("get diffs: rpc endpoint not served until phase 3")
}

func (c *RPCClient) GetMempool(ctx context.Context) ([]*types.DevshardTx, error) {
	if !c.Uses(EndpointMempool) {
		return c.HTTPClient.GetMempool(ctx)
	}
	return nil, fmt.Errorf("get mempool: rpc endpoint not served until phase 3")
}

func (c *RPCClient) GossipNonce(ctx context.Context, nonce uint64, stateHash, stateSig []byte, slotID uint32) error {
	if !c.Uses(EndpointGossip) {
		return c.HTTPClient.GossipNonce(ctx, nonce, stateHash, stateSig, slotID)
	}
	payload, err := proto.Marshal(&rpcpb.GossipNonceRequest{
		Nonce:     nonce,
		StateHash: stateHash,
		StateSig:  stateSig,
		SlotId:    slotID,
	})
	if err != nil {
		return err
	}
	return c.gossipSigned(ctx, c.config.GossipTimeout, payload, func(ctx context.Context, req *connect.Request[rpcpb.SignedEnvelope]) error {
		_, err := c.gossip.Nonce(ctx, req)
		return err
	})
}

func (c *RPCClient) GossipTxs(ctx context.Context, txs []*types.DevshardTx) error {
	if !c.Uses(EndpointGossip) {
		return c.HTTPClient.GossipTxs(ctx, txs)
	}
	txBytes, err := DevshardTxsToBytes(txs)
	if err != nil {
		return fmt.Errorf("encode txs: %w", err)
	}
	payload, err := proto.Marshal(&rpcpb.GossipTxsRequest{Txs: txBytes})
	if err != nil {
		return err
	}
	return c.gossipSigned(ctx, c.config.GossipTimeout, payload, func(ctx context.Context, req *connect.Request[rpcpb.SignedEnvelope]) error {
		_, err := c.gossip.Txs(ctx, req)
		return err
	})
}

func (c *RPCClient) gossipSigned(ctx context.Context, timeout time.Duration, payload []byte, call func(context.Context, *connect.Request[rpcpb.SignedEnvelope]) error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return rpcRetry(ctx, func() error {
		env, err := c.signEnvelope(payload)
		if err != nil {
			return err
		}
		req, err := tokenRequest(c, env)
		if err != nil {
			return err
		}
		return call(ctx, req)
	})
}

func (c *RPCClient) signEnvelope(payload []byte) (*rpcpb.SignedEnvelope, error) {
	if c.signer == nil {
		return nil, fmt.Errorf("rpc client: signer is required")
	}
	return SignEnvelope(c.signer, c.escrowID, payload, time.Now().Unix())
}

func (c *RPCClient) SeedHeightSync(ctx context.Context) (ok bool, err error) {
	if !c.Uses(EndpointSeed) {
		return c.HTTPClient.SeedHeightSync(ctx)
	}
	return false, fmt.Errorf("seed height-sync: rpc endpoint not served until phase 3")
}

func (c *RPCClient) HeightSyncRepair(ctx context.Context, req *heightsync.RepairRequest) (*heightsync.RepairResponse, error) {
	if !c.Uses(EndpointRepair) {
		return c.HTTPClient.HeightSyncRepair(ctx, req)
	}
	return nil, fmt.Errorf("height-sync repair: rpc endpoint not served until phase 3")
}

func (c *RPCClient) SendVerifyTimeout(ctx context.Context, req VerifyTimeoutRequest) (*VerifyTimeoutResponse, error) {
	if !c.Uses(EndpointVerifyTimeout) {
		return c.HTTPClient.SendVerifyTimeout(ctx, req)
	}
	return nil, fmt.Errorf("verify-timeout: rpc endpoint not served until phase 3")
}

func (c *RPCClient) SendVerifyErrorMiss(ctx context.Context, req VerifyErrorMissRequest) (*VerifyErrorMissResponse, error) {
	if !c.Uses(EndpointVerifyErrorMiss) {
		return c.HTTPClient.SendVerifyErrorMiss(ctx, req)
	}
	return nil, fmt.Errorf("verify-error-miss: rpc endpoint not served until phase 3")
}

func (c *RPCClient) ChallengeReceipt(ctx context.Context, inferenceID uint64, payload *host.InferencePayload, diffs []types.Diff) ([]byte, []*types.DevshardTx, error) {
	if !c.Uses(EndpointChallengeReceipt) {
		return c.HTTPClient.ChallengeReceipt(ctx, inferenceID, payload, diffs)
	}
	return nil, nil, fmt.Errorf("challenge-receipt: rpc endpoint not served until phase 3")
}

// cloneWithSigner keeps the PeerConn and endpoint set so a later opted-in
// method (repair, verify) does not silently fall back to JSON (finding 7).
// ownsConn is false: same as WithoutAdmission. Repair does not Close the
// clone; bumping refs would leak. SetPeerClients is still map[int]*HTTPClient
// until Phase 3 stores SelectTransport results.
func (c *RPCClient) cloneWithSigner(signer signing.Signer, timeout time.Duration) *RPCClient {
	if c == nil {
		return nil
	}
	out := *c
	if c.HTTPClient != nil {
		out.HTTPClient = c.HTTPClient.cloneWithSigner(signer, timeout)
	}
	out.ownsConn = false
	out.closeOnce = new(sync.Once)
	return &out
}

var (
	_ gossip.PeerClient  = (*HTTPClient)(nil)
	_ gossip.PeerClient  = (*RPCClient)(nil)
	_ gossip.DiffFetcher = (*HTTPClient)(nil)
	_ gossip.DiffFetcher = (*RPCClient)(nil)
)
