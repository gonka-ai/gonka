package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"common/validation"
	"devshard/gossip"
	"devshard/heightsync"
	"devshard/host"
	"devshard/observability"
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
	// sessionQuery is a second SessionServiceClient with the 10 MiB catch-up
	// read cap (DefaultMaxBodySize). Connect applies WithReadMaxBytes per
	// client, not per RPC, so GetDiffs / GetMempool cannot share the 16 KiB
	// session client.
	sessionQuery rpcpbconnect.SessionServiceClient
	// sessionLarge is a third SessionServiceClient with the 10 MiB dispute
	// read cap (VerifyTimeout, VerifyErrorMiss, and a decoded
	// ChallengeReceipt). Catch-up pages use challengeLarge with the same
	// gzip options.
	sessionLarge rpcpbconnect.SessionServiceClient
	// challengeLarge sends ChallengeReceipt: a nil-payload catch-up page and
	// the page that carries the payload. The codec writes the page into
	// Connect's send buffer, so no separate body is kept while the executor
	// applies it, and Connect gzips that buffer once it reaches
	// MinGzipBodyBytes. The read cap is 10 MiB.
	challengeLarge, challengeLargeGRPC challengeClient
	// sessionChat is SessionService.Chat: 10 MiB envelope, no per-frame gzip.
	sessionChat rpcpbconnect.SessionServiceClient
	gossip      rpcpbconnect.GossipServiceClient
	// payload is GetPayload stubs, one per rpcPayloadReadBuckets entry.
	// Connect applies WithReadMaxBytes per client, not per RPC, so
	// payloadClient picks the smallest bucket >= PayloadReadLimit.
	payload          [rpcPayloadReadBucketCount]rpcpbconnect.PayloadServiceClient
	payloadMu        *sync.Mutex
	sessionGRPC      rpcpbconnect.SessionServiceClient
	sessionQueryGRPC rpcpbconnect.SessionServiceClient
	sessionLargeGRPC rpcpbconnect.SessionServiceClient
	sessionChatGRPC  rpcpbconnect.SessionServiceClient
	gossipGRPC       rpcpbconnect.GossipServiceClient
	payloadGRPC      [rpcPayloadReadBucketCount]rpcpbconnect.PayloadServiceClient
	// closeOnce is a pointer so WithoutAdmission can copy RPCClient without
	// copying a sync.Once (go vet copylocks).
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
		payloadMu:  new(sync.Mutex),
	}
	if conn != nil && httpClient != nil {
		conn.addDoor(httpClient.escrowID)
		// Registry-backed conns (refs > 0) start here so the creator door
		// is registered before the first Attach. NewPeerConn tests (refs 0)
		// still call Start themselves.
		if conn.refs.Load() > 0 {
			conn.Start()
		}
		base := conn.cfg.connectBase(httpClient.escrowID)
		opts := connectClientOptions(conn.cfg.ReadMaxBytes)
		c.session = rpcpbconnect.NewSessionServiceClient(conn.http, base, opts...)
		c.sessionQuery = rpcpbconnect.NewSessionServiceClient(conn.http, base, connectClientOptions(DefaultRPCQueryReadMaxBytes)...)
		c.sessionLarge = rpcpbconnect.NewSessionServiceClient(conn.http, base, connectClientOptions(DefaultRPCLargeReadMaxBytes)...)
		c.challengeLarge = newChallengeClient(conn.http, base, connectClientOptions(DefaultRPCLargeReadMaxBytes))
		c.sessionChat = rpcpbconnect.NewSessionServiceClient(conn.http, base, chatClientOptions(conn.cfg.ChatCompressMinBytes)...)
		c.gossip = rpcpbconnect.NewGossipServiceClient(conn.http, base, opts...)
		if conn.cfg.GRPC {
			c.sessionGRPC = rpcpbconnect.NewSessionServiceClient(conn.httpFor(true), base, maybeGRPC(opts, true)...)
			c.sessionQueryGRPC = rpcpbconnect.NewSessionServiceClient(conn.httpFor(true), base, maybeGRPC(connectClientOptions(DefaultRPCQueryReadMaxBytes), true)...)
			c.sessionLargeGRPC = rpcpbconnect.NewSessionServiceClient(conn.httpFor(true), base, maybeGRPC(connectClientOptions(DefaultRPCLargeReadMaxBytes), true)...)
			c.challengeLargeGRPC = newChallengeClient(conn.httpFor(true), base, maybeGRPC(connectClientOptions(DefaultRPCLargeReadMaxBytes), true))
			c.sessionChatGRPC = rpcpbconnect.NewSessionServiceClient(conn.httpFor(true), base, maybeGRPC(chatClientOptions(conn.cfg.ChatCompressMinBytes), true)...)
			c.gossipGRPC = rpcpbconnect.NewGossipServiceClient(conn.httpFor(true), base, maybeGRPC(opts, true)...)
		}
	}
	return c
}

// Uses is whether this client sends `name` over Connect. Opt-in
// (EndpointSet.Has) is not enough: a name stays HTTP until it is on
// attachRPCEndpoints.
func (c *RPCClient) Uses(name string) bool {
	return c != nil && c.endpoints.Has(name) && isAttachRPCEndpoint(name)
}

// WaitReady blocks until Attach has published a live token or ctx is done.
// It aims the next first-handshake Attach at this client's escrow. If no
// live escrow remains as an Attach door (all settled / not found), it
// returns ErrPeerNotReady immediately so the caller can use JSON.
func (c *RPCClient) WaitReady(ctx context.Context) error {
	if c == nil || c.conn == nil {
		return ErrPeerNotReady
	}
	if c.HTTPClient != nil {
		c.conn.preferDoor(c.HTTPClient.escrowID)
	}
	for {
		if c.conn.Ready() {
			return nil
		}
		if !c.conn.hasAttachDoor() {
			return fmt.Errorf("%w: no attach door", ErrPeerNotReady)
		}
		if err := c.conn.ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", ErrPeerNotReady, err)
		}
		wait := c.conn.waiter()
		if c.conn.Ready() {
			return nil
		}
		if !c.conn.hasAttachDoor() {
			return fmt.Errorf("%w: no attach door", ErrPeerNotReady)
		}
		if err := c.conn.ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", ErrPeerNotReady, err)
		}
		select {
		case <-ctx.Done():
			if c.conn.Ready() {
				return nil
			}
			return fmt.Errorf("%w: %v", ErrPeerNotReady, ctx.Err())
		case <-c.conn.ctx.Done():
			if c.conn.Ready() {
				return nil
			}
			return fmt.Errorf("%w: %v", ErrPeerNotReady, c.conn.ctx.Err())
		case <-wait:
		}
	}
}

// DoorRefused reports that the remote host closed this client's escrow
// door at Attach with a dead-door answer. That is the host's claim.
func (c *RPCClient) DoorRefused() bool {
	if c == nil || c.conn == nil || c.HTTPClient == nil {
		return false
	}
	return c.conn.doorKilled(c.HTTPClient.escrowID)
}

// DoorRefusals counts dead-door answers for this client's escrow; unlike
// DoorRefused it survives another caller's ReviveDoor.
func (c *RPCClient) DoorRefusals() uint64 {
	if c == nil || c.conn == nil || c.HTTPClient == nil {
		return 0
	}
	return c.conn.doorRefusals(c.HTTPClient.escrowID)
}

// ReviveDoor lets the next Attach try this client's refused escrow door again.
func (c *RPCClient) ReviveDoor() {
	if c == nil || c.conn == nil || c.HTTPClient == nil {
		return
	}
	c.conn.reviveDoor(c.HTTPClient.escrowID)
}

func (c *RPCClient) Close() {
	if c == nil || c.closeOnce == nil {
		return
	}
	c.closeOnce.Do(func() {
		if c.conn == nil {
			return
		}
		if c.HTTPClient != nil {
			c.conn.dropDoor(c.HTTPClient.escrowID)
		}
		if c.ownsConn {
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
	out.retainDoor()
	return &out
}

func tokenRequest[T any](ctx context.Context, c *RPCClient, msg *T) (*connect.Request[T], error) {
	if c.conn == nil || !c.conn.Ready() {
		return nil, ErrPeerNotReady
	}
	tok := c.conn.LiveToken()
	if len(tok) == 0 {
		return nil, ErrPeerNotReady
	}
	req := connect.NewRequest(msg)
	SetSessionHeader(req.Header(), tok)
	// W3C traceparent + X-Request-Id so versiond EchoMiddleware joins the
	// gateway span. Connect/gRPC carries these as HTTP/2 headers through the
	// router; without them the host opens a new root trace.
	observability.InjectOutboundHeaders(ctx, req.Header())
	return req, nil
}

// unauthenticatedRetryDelay is the pause before the single Unauthenticated
// retry when the token is still live. It covers the usual apply lag. A
// cleared token does not use this pause as its deadline: the retry waits
// for the next Attach inside the non-inference budget.
const unauthenticatedRetryDelay = 200 * time.Millisecond

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
		ra := connectRetryAfter(err)
		if ra > 0 {
			if ra > remaining {
				return err
			}
			sleep = ra
		} else if sleep > remaining {
			sleep = remaining
		}
		if ra == 0 && connect.CodeOf(err) == connect.CodeUnauthenticated {
			if unauthenticatedRetryDelay > remaining {
				return err
			}
			sleep = unauthenticatedRetryDelay
		}
		if err := sleepContext(ctx, sleep); err != nil {
			return last
		}
		if delay < nonInferenceRetryBudget {
			delay *= 2
		}
	}
}

func (c *RPCClient) withPeerBudget(ctx context.Context, procedure string, fn func() error) error {
	if c != nil && c.conn != nil {
		if err := c.conn.takePeerBudget(ctx, procedure); err != nil {
			return err
		}
	}
	err := fn()
	if isQuotaResourceExhausted(err) && c != nil && c.conn != nil {
		c.conn.refundPeerBudget(procedure)
	}
	return err
}

func (c *RPCClient) rpcAttempt(ctx context.Context, procedure string, fn func() error) error {
	// Same admission as HTTP doPostRaw: one check for the logical call, then
	// the limiter sees only the final result. A local rejection is not a
	// host fault.
	path := ""
	if c != nil && c.HTTPClient != nil {
		path = connectAdmissionPath(c.escrowID, procedure)
		if err := c.allowRequest(path); err != nil {
			return err
		}
	}
	attempt := 0
	err := rpcRetry(ctx, func() error {
		attempt++
		// The 200ms pause already ran. If Watch cleared the token during
		// that pause, wait for the replacement Attach instead of failing
		// the retry with ErrPeerNotReady.
		if attempt > 1 && c != nil && c.conn != nil && !c.conn.Ready() {
			if err := c.waitReadyForRetry(ctx); err != nil {
				return err
			}
		}
		return c.withPeerBudget(ctx, procedure, fn)
	})
	if c != nil && c.HTTPClient != nil {
		if err != nil {
			c.observeConnect(path, err)
		} else {
			c.observeResult(path, http.StatusOK)
		}
	}
	return err
}

// waitReadyForRetry blocks until Attach publishes a token or the
// non-inference retry budget runs out. The caller's deadline still applies.
func (c *RPCClient) waitReadyForRetry(ctx context.Context) error {
	waitCtx, cancel := context.WithDeadline(ctx, nonInferenceRetryDeadline(ctx))
	defer cancel()
	return c.WaitReady(waitCtx)
}

const maxUnauthenticatedRPCRetries = 1

// isRetryableRPC is the Connect retry policy. Unavailable and 429-style
// ResourceExhausted use the shared 5 s budget. Message-size ResourceExhausted
// fails fast (IsRetryableNonInference). Unauthenticated is one extra attempt
// for a token rotation race; a stable unauthenticated session fails fast.
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
	return []connect.ClientOption{
		connect.WithReadMaxBytes(maxBytes),
		withGzipReadCap(maxBytes),
		connect.WithSendGzip(),
		connect.WithCompressMinBytes(MinGzipBodyBytes),
	}
}

func maybeGRPC(opts []connect.ClientOption, on bool) []connect.ClientOption {
	if !on {
		return opts
	}
	out := make([]connect.ClientOption, 0, len(opts)+1)
	out = append(out, opts...)
	return append(out, connect.WithGRPC())
}

func (c *RPCClient) grpcOn() bool {
	return c != nil && c.conn != nil && c.conn.useGRPC()
}

func (c *RPCClient) sessionClient() rpcpbconnect.SessionServiceClient {
	if c.grpcOn() && c.sessionGRPC != nil {
		return c.sessionGRPC
	}
	return c.session
}

func (c *RPCClient) sessionQueryClient() rpcpbconnect.SessionServiceClient {
	if c.grpcOn() && c.sessionQueryGRPC != nil {
		return c.sessionQueryGRPC
	}
	return c.sessionQuery
}

func (c *RPCClient) sessionLargeClient() rpcpbconnect.SessionServiceClient {
	if c.grpcOn() && c.sessionLargeGRPC != nil {
		return c.sessionLargeGRPC
	}
	return c.sessionLarge
}

func (c *RPCClient) sessionChatClient() rpcpbconnect.SessionServiceClient {
	if c.grpcOn() && c.sessionChatGRPC != nil {
		return c.sessionChatGRPC
	}
	return c.sessionChat
}

func (c *RPCClient) gossipClient() rpcpbconnect.GossipServiceClient {
	if c.grpcOn() && c.gossipGRPC != nil {
		return c.gossipGRPC
	}
	return c.gossip
}

// chatClientOptions gzips the request envelope (prompt + catch-up diffs) like
// the unary clients and HTTP Send, from MinGzipBodyBytes up, or from compressMinBytes when it is positive. withGzipReadCap keeps that gzip registered
// for WithSendGzip and stops response inflation at the read cap. The handler
// never compresses ChatFrames, so response chunks are still one application
// gzip stream and are not compressed twice.
func chatClientOptions(compressMinBytes int) []connect.ClientOption {
	options := []connect.ClientOption{
		connect.WithReadMaxBytes(int(DefaultMaxBodySize)),
		withGzipReadCap(int(DefaultMaxBodySize)),
		connect.WithSendGzip(),
		connect.WithCompressMinBytes(MinGzipBodyBytes),
	}
	if compressMinBytes > 0 {
		options = append(options, connect.WithCompressMinBytes(compressMinBytes))
	}
	return options
}

func (c *RPCClient) GetSignatures(ctx context.Context, nonce uint64) (map[uint32][]byte, error) {
	if !c.Uses(EndpointSignatures) {
		return c.HTTPClient.GetSignatures(ctx, nonce)
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.QueryTimeout)
	defer cancel()
	var out map[uint32][]byte
	err := c.rpcAttempt(ctx, rpcpbconnect.SessionServiceGetSignaturesProcedure, func() error {
		req, err := tokenRequest(ctx, c, &rpcpb.GetSignaturesRequest{Nonce: nonce})
		if err != nil {
			return err
		}
		resp, err := c.sessionClient().GetSignatures(ctx, req)
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
	ctx, cancel := context.WithTimeout(ctx, c.config.QueryTimeout)
	defer cancel()
	var diffs []types.Diff
	err := c.rpcAttempt(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, func() error {
		req, err := tokenRequest(ctx, c, &rpcpb.GetDiffsRequest{From: from, To: to})
		if err != nil {
			return err
		}
		resp, err := c.sessionQueryClient().GetDiffs(ctx, req)
		if err != nil {
			return err
		}
		records := resp.Msg.GetRecords()
		diffs = make([]types.Diff, len(records))
		for i, rec := range records {
			d, dErr := DiffFromJSON(DiffJSONFromProto(rec.GetDiff()))
			if dErr != nil {
				return fmt.Errorf("decode diff %d: %w", i, dErr)
			}
			diffs[i] = d
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("get diffs: %w", err)
	}
	return diffs, nil
}

// SessionHead is the executor tip read for a refused-timeout retry. It calls
// SessionService.GetState on the peer session, using the gRPC codec when the
// connection has it. The session token and AllowsSender gate that RPC.
func (c *RPCClient) SessionHead(ctx context.Context) (uint64, []byte, error) {
	if c == nil || !c.Uses(EndpointState) {
		return 0, nil, fmt.Errorf("session head requires the state rpc")
	}
	timeout := c.config.QueryTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var nonce uint64
	var root []byte
	err := c.rpcAttempt(ctx, rpcpbconnect.SessionServiceGetStateProcedure, func() error {
		req, err := tokenRequest(ctx, c, &rpcpb.GetStateRequest{})
		if err != nil {
			return err
		}
		resp, err := c.sessionClient().GetState(ctx, req)
		if err != nil {
			return err
		}
		nonce = resp.Msg.GetNonce()
		root = append([]byte(nil), resp.Msg.GetStateRoot()...)
		return nil
	})
	if err != nil {
		return 0, nil, fmt.Errorf("get state: %w", err)
	}
	return nonce, root, nil
}

func (c *RPCClient) GetMempool(ctx context.Context) ([]*types.DevshardTx, error) {
	if !c.Uses(EndpointMempool) {
		return c.HTTPClient.GetMempool(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.QueryTimeout)
	defer cancel()
	var txs []*types.DevshardTx
	err := c.rpcAttempt(ctx, rpcpbconnect.SessionServiceGetMempoolProcedure, func() error {
		req, err := tokenRequest(ctx, c, &rpcpb.GetMempoolRequest{})
		if err != nil {
			return err
		}
		resp, err := c.sessionQueryClient().GetMempool(ctx, req)
		if err != nil {
			return err
		}
		decoded, dErr := DevshardTxsFromBytes(resp.Msg.GetTxs())
		if dErr != nil {
			return dErr
		}
		txs = decoded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("get mempool: %w", err)
	}
	return txs, nil
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
	return c.gossipSigned(ctx, c.config.GossipTimeout, rpcpbconnect.GossipServiceNonceProcedure, payload, func(ctx context.Context, req *connect.Request[rpcpb.SignedEnvelope]) error {
		_, err := c.gossipClient().Nonce(ctx, req)
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
	return c.gossipSigned(ctx, c.config.GossipTimeout, rpcpbconnect.GossipServiceTxsProcedure, payload, func(ctx context.Context, req *connect.Request[rpcpb.SignedEnvelope]) error {
		_, err := c.gossipClient().Txs(ctx, req)
		return err
	})
}

func (c *RPCClient) gossipSigned(ctx context.Context, timeout time.Duration, procedure string, payload []byte, call func(context.Context, *connect.Request[rpcpb.SignedEnvelope]) error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.rpcAttempt(ctx, procedure, func() error {
		env, err := c.signEnvelope(payload)
		if err != nil {
			return err
		}
		req, err := tokenRequest(ctx, c, env)
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
	if c == nil || c.heightSyncPeerTips == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.heightSeedTimeout())
	defer cancel()
	// HTTP seed admits once and does not retry. Status results stay off the
	// limiter (height-sync path); a dial failure is still a transport fault.
	path := connectAdmissionPath(c.escrowID, rpcpbconnect.SessionServiceSeedHeightSyncProcedure)
	if err := c.allowRequest(path); err != nil {
		return false, err
	}
	err = c.withPeerBudget(ctx, rpcpbconnect.SessionServiceSeedHeightSyncProcedure, func() error {
		env, err := c.signEnvelope(nil)
		if err != nil {
			return err
		}
		req, err := tokenRequest(ctx, c, env)
		if err != nil {
			return err
		}
		resp, err := c.sessionClient().SeedHeightSync(ctx, req)
		if err != nil {
			return err
		}
		sec := HeightSyncSectionFromProto(resp.Msg.GetHeightSync())
		if sec == nil || !heightsync.IsAnchorSection(sec) {
			return nil
		}
		sec.Direction = "response"
		c.ingestResponseHeightSync(sec, 0, "RPC SeedHeightSync")
		_, _, ok = c.heightSyncPeerTips.OriginSignedBlobFor(
			strings.TrimSpace(sec.OriginatorSenderID),
			sec.MainnetHeight,
		)
		return nil
	})
	if err != nil {
		c.observeConnect(path, err)
	} else {
		c.observeResult(path, http.StatusOK)
	}
	return ok, err
}

func (c *RPCClient) HeightSyncRepair(ctx context.Context, req *heightsync.RepairRequest) (*heightsync.RepairResponse, error) {
	if !c.Uses(EndpointRepair) {
		return c.HTTPClient.HeightSyncRepair(ctx, req)
	}
	timeout := c.config.QueryTimeout
	if timeout <= 0 || timeout > DefaultRepairTimeout {
		timeout = DefaultRepairTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	payload, err := proto.Marshal(RepairRequestToProto(req))
	if err != nil {
		return nil, err
	}
	var out *heightsync.RepairResponse
	err = c.rpcAttempt(ctx, rpcpbconnect.SessionServiceRepairHeightSyncProcedure, func() error {
		env, err := c.signEnvelope(payload)
		if err != nil {
			return err
		}
		creq, err := tokenRequest(ctx, c, env)
		if err != nil {
			return err
		}
		resp, err := c.sessionClient().RepairHeightSync(ctx, creq)
		if err != nil {
			return err
		}
		out = RepairResponseFromProto(resp.Msg)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("height-sync repair: %w", err)
	}
	return out, nil
}

func (c *RPCClient) SendVerifyTimeout(ctx context.Context, req VerifyTimeoutRequest) (*VerifyTimeoutResponse, error) {
	if !c.Uses(EndpointVerifyTimeout) {
		return c.HTTPClient.SendVerifyTimeout(ctx, req)
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.VerifyTimeout)
	defer cancel()
	payload, err := proto.Marshal(VerifyTimeoutRequestToProto(req))
	if err != nil {
		return nil, err
	}
	var out *VerifyTimeoutResponse
	err = c.rpcAttempt(ctx, rpcpbconnect.SessionServiceVerifyTimeoutProcedure, func() error {
		env, err := c.signEnvelope(payload)
		if err != nil {
			return err
		}
		creq, err := tokenRequest(ctx, c, env)
		if err != nil {
			return err
		}
		resp, err := c.sessionLargeClient().VerifyTimeout(ctx, creq)
		if err != nil {
			return err
		}
		out = VerifyTimeoutResponseFromProto(resp.Msg)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("verify-timeout: %w", err)
	}
	return out, nil
}

func (c *RPCClient) SendVerifyErrorMiss(ctx context.Context, req VerifyErrorMissRequest) (*VerifyErrorMissResponse, error) {
	if !c.Uses(EndpointVerifyErrorMiss) {
		return c.HTTPClient.SendVerifyErrorMiss(ctx, req)
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.VerifyTimeout)
	defer cancel()
	payload, err := proto.Marshal(VerifyErrorMissRequestToProto(req))
	if err != nil {
		return nil, err
	}
	var out *VerifyErrorMissResponse
	err = c.rpcAttempt(ctx, rpcpbconnect.SessionServiceVerifyErrorMissProcedure, func() error {
		env, err := c.signEnvelope(payload)
		if err != nil {
			return err
		}
		creq, err := tokenRequest(ctx, c, env)
		if err != nil {
			return err
		}
		resp, err := c.sessionLargeClient().VerifyErrorMiss(ctx, creq)
		if err != nil {
			return err
		}
		out = VerifyErrorMissResponseFromProto(resp.Msg)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("verify-error-miss: %w", err)
	}
	return out, nil
}

func (c *RPCClient) ChallengeReceipt(ctx context.Context, inferenceID uint64, payload *host.InferencePayload, diffs []types.Diff) ([]byte, []*types.DevshardTx, error) {
	if !c.Uses(EndpointChallengeReceipt) {
		return c.HTTPClient.ChallengeReceipt(ctx, inferenceID, payload, diffs)
	}
	raw, err := recordRefusalBody(false, ctx, func() ([]byte, error) {
		djList := make([]DiffJSON, len(diffs))
		for i, d := range diffs {
			dj, err := DiffToJSON(d)
			if err != nil {
				return nil, fmt.Errorf("encode diff %d: %w", i, err)
			}
			djList[i] = dj
		}
		inner := ChallengeReceiptRequestToProto(ChallengeReceiptRequest{
			InferenceID: inferenceID,
			Payload:     PayloadToJSON(payload),
			Diffs:       djList,
		})
		return proto.Marshal(inner)
	})
	if err != nil {
		return nil, nil, err
	}
	return c.challengeReceiptRaw(ctx, raw)
}

// ChallengeReceiptWires sends the payload and one page of stored wires. The
// page is not decoded. It is encoded into Connect's send buffer and dropped;
// a retry reads it again through reload. A nil reload keeps the page.
func (c *RPCClient) ChallengeReceiptWires(ctx context.Context, inferenceID uint64, payload *host.InferencePayload, diffs []types.DiffWire, reload host.WireReload) ([]byte, []*types.DevshardTx, error) {
	if !c.Uses(EndpointChallengeReceipt) {
		return c.HTTPClient.ChallengeReceiptWires(ctx, inferenceID, payload, diffs, reload)
	}
	body, err := newChallengeBody(inferenceID, payload, diffs)
	if err != nil {
		return nil, nil, err
	}
	recordRefusalSize(false, ctx, body.size())
	body.wires = nil
	page := &challengePage{wires: diffs, reload: reload}
	ctx, cancel := context.WithTimeout(ctx, c.config.VerifyTimeout)
	defer cancel()
	resp, err := c.sendChallenge(ctx, c.challengeLargeClient(), body, page)
	if err != nil {
		return nil, nil, fmt.Errorf("challenge-receipt: %w", err)
	}
	mempool, err := DevshardTxsFromBytes(resp.GetMempool())
	if err != nil {
		return nil, nil, fmt.Errorf("challenge-receipt: %w", err)
	}
	return resp.GetReceipt(), mempool, nil
}

func (c *RPCClient) challengeReceiptRaw(ctx context.Context, raw []byte) ([]byte, []*types.DevshardTx, error) {
	ctx, cancel := context.WithTimeout(ctx, c.config.VerifyTimeout)
	defer cancel()
	var receipt []byte
	var mempool []*types.DevshardTx
	err := c.rpcAttempt(ctx, rpcpbconnect.SessionServiceChallengeReceiptProcedure, func() error {
		env, err := c.signEnvelope(raw)
		if err != nil {
			return err
		}
		creq, err := tokenRequest(ctx, c, env)
		if err != nil {
			return err
		}
		resp, err := c.sessionLargeClient().ChallengeReceipt(ctx, creq)
		if err != nil {
			return err
		}
		decoded, dErr := DevshardTxsFromBytes(resp.Msg.GetMempool())
		if dErr != nil {
			return dErr
		}
		receipt = resp.Msg.GetReceipt()
		mempool = decoded
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("challenge-receipt: %w", err)
	}
	return receipt, mempool, nil
}

// ChallengeCatchUp sends one encoded page with a nil payload and drops the
// response.
func (c *RPCClient) ChallengeCatchUp(ctx context.Context, inferenceID uint64, diffs []types.DiffWire, protocolVersion string) error {
	send, err := c.PrepareChallengeCatchUp(inferenceID, diffs, nil, protocolVersion)
	if err != nil {
		return err
	}
	return send(ctx)
}

// PrepareChallengeCatchUp returns the send for one page. Each attempt encodes
// the page into Connect's send buffer. With a reload, the first attempt drops
// the page and a retry reads it again, so the send func does not reference
// diffs while the executor applies them. A nil reload keeps the page.
func (c *RPCClient) PrepareChallengeCatchUp(inferenceID uint64, diffs []types.DiffWire, reload host.WireReload, protocolVersion string) (func(context.Context) error, error) {
	if !c.Uses(EndpointChallengeReceipt) {
		return c.HTTPClient.PrepareChallengeCatchUp(inferenceID, diffs, reload, protocolVersion)
	}
	body, err := newChallengeBody(inferenceID, nil, diffs)
	if err != nil {
		return nil, err
	}
	recordRefusalSize(true, nil, body.size())
	body.wires = nil
	page := &challengePage{wires: diffs, reload: reload}
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, c.config.VerifyTimeout)
		defer cancel()
		if _, err := c.sendChallenge(ctx, c.challengeLargeClient(), body, page); err != nil {
			return fmt.Errorf("challenge-receipt: %w", err)
		}
		return nil
	}, nil
}

// ChallengeReceiptWireBody is the Connect payload of a challenge that carries
// the prompt and one page of stored wires. Each diff's Txs is the stored blob
// with the nonce field prepended. The page is not decoded. RPCClient encodes
// these bytes straight into the envelope it sends.
func ChallengeReceiptWireBody(inferenceID uint64, payload *host.InferencePayload, diffs []types.DiffWire) ([]byte, error) {
	body, err := newChallengeBody(inferenceID, payload, diffs)
	if err != nil {
		return nil, err
	}
	return body.appendTo(nil), nil
}

// ChallengeCatchUpBody is the Connect payload of a nil-payload catch-up page.
// Each diff's Txs is the stored blob with the nonce field prepended.
func ChallengeCatchUpBody(inferenceID uint64, diffs []types.DiffWire) ([]byte, error) {
	return ChallengeReceiptWireBody(inferenceID, nil, diffs)
}

// VerifyTimeout must be on *RPCClient: the HTTP wrapper calls SendVerifyTimeout
// on its own receiver, which would skip Connect.
func (c *RPCClient) VerifyTimeout(ctx context.Context, inferenceID uint64, reason types.TimeoutReason, payload *host.InferencePayload, diffs []types.Diff, artifacts host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	_ = artifacts
	var djList []DiffJSON
	if len(diffs) > 0 {
		djList = make([]DiffJSON, len(diffs))
		for i, d := range diffs {
			dj, err := DiffToJSON(d)
			if err != nil {
				return false, nil, 0, nil, "", fmt.Errorf("encode diff %d: %w", i, err)
			}
			djList[i] = dj
		}
	}
	resp, err := c.SendVerifyTimeout(ctx, VerifyTimeoutRequest{
		InferenceID: inferenceID,
		Reason:      TimeoutReasonToString(reason),
		Payload:     timeoutVotePayload(reason, payload),
		Diffs:       djList,
	})
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	mempool, err := DevshardTxsFromBytes(resp.Mempool)
	if err != nil {
		return false, nil, 0, nil, "", fmt.Errorf("decode mempool: %w", err)
	}
	return resp.Accept, resp.Signature, resp.VoterSlot, mempool, resp.RejectCause, nil
}

func (c *RPCClient) VerifyErrorMiss(ctx context.Context, inferenceID uint64, diffs []types.Diff, artifacts host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	var djList []DiffJSON
	if len(diffs) > 0 {
		djList = make([]DiffJSON, len(diffs))
		for i, d := range diffs {
			dj, err := DiffToJSON(d)
			if err != nil {
				return false, nil, 0, nil, "", fmt.Errorf("encode diff %d: %w", i, err)
			}
			djList[i] = dj
		}
	}
	resp, err := c.SendVerifyErrorMiss(ctx, VerifyErrorMissRequest{
		InferenceID:     inferenceID,
		Diffs:           djList,
		FinishTx:        artifacts.FinishTx,
		ResponsePayload: artifacts.ResponsePayload,
	})
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	mempool, err := DevshardTxsFromBytes(resp.Mempool)
	if err != nil {
		return false, nil, 0, nil, "", fmt.Errorf("decode mempool: %w", err)
	}
	return resp.Accept, resp.Signature, resp.VoterSlot, mempool, resp.RejectCause, nil
}

const (
	rpcPayloadRead32          = 32 << 20
	rpcPayloadRead256         = 256 << 20
	rpcPayloadReadBucketCount = 4
)

// rpcPayloadReadBuckets is WithReadMaxBytes on the cached GetPayload stubs.
// payloadClient picks the smallest entry >= PayloadReadLimit(maxBytes).
var rpcPayloadReadBuckets = [rpcPayloadReadBucketCount]int{
	rpcPayloadRead32,
	DefaultRPCPayloadMaxBytes,
	rpcPayloadRead256,
	DefaultRPCPayloadSendMaxBytes,
}

func payloadReadBucketIndex(maxBytes int64) int {
	need := int(validation.PayloadReadLimit(maxBytes))
	for i, n := range rpcPayloadReadBuckets {
		if n >= need {
			return i
		}
	}
	return rpcPayloadReadBucketCount - 1
}

func payloadReadBucket(maxBytes int64) int {
	return rpcPayloadReadBuckets[payloadReadBucketIndex(maxBytes)]
}

// payloadDecodedSize is the per-inference pin: prompt + response bytes after
// decode. HTTP GET bounds the JSON body with cappedReader; Connect
// WithReadMaxBytes is the stub bucket (32 / 64 / 256 / 512 MiB), so this
// is what keeps the two transports on the same limit.
func payloadDecodedSize(resp *rpcpb.GetPayloadResponse) int64 {
	if resp == nil {
		return 0
	}
	return int64(len(resp.GetPromptPayload())) + int64(len(resp.GetResponsePayload()))
}

func errPayloadDecodedTooLarge(n, limit int64) error {
	// "larger than configured max" is isConnectMessageTooLarge: not retryable.
	return connect.NewError(connect.CodeResourceExhausted,
		fmt.Errorf("payload decoded size %d is larger than configured max %d", n, limit))
}

// payloadClient is the Connect GetPayload stub for this call's read cap.
// Connect applies WithReadMaxBytes per client, not per RPC. The need is
// PayloadReadLimit(maxBytes); the stub is the smallest prebuilt bucket
// that is still >= that need (32 / 64 / 256 / 512 MiB).
func (c *RPCClient) payloadClient(maxBytes int64) (rpcpbconnect.PayloadServiceClient, error) {
	if c == nil {
		return nil, fmt.Errorf("no peer connection")
	}
	if c.payloadMu != nil {
		c.payloadMu.Lock()
		defer c.payloadMu.Unlock()
	}
	i := payloadReadBucketIndex(maxBytes)
	grpc := c.grpcOn()
	if grpc && c.payloadGRPC[i] != nil {
		return c.payloadGRPC[i], nil
	}
	if !grpc && c.payload[i] != nil {
		return c.payload[i], nil
	}
	if c.conn == nil || c.HTTPClient == nil {
		return nil, fmt.Errorf("no peer connection")
	}
	base := c.conn.cfg.connectBase(c.HTTPClient.escrowID)
	client := rpcpbconnect.NewPayloadServiceClient(c.conn.httpFor(grpc), base, maybeGRPC(connectClientOptions(rpcPayloadReadBuckets[i]), grpc)...)
	if grpc {
		c.payloadGRPC[i] = client
	} else {
		c.payload[i] = client
	}
	return client, nil
}

// GetPayload fetches inference payloads over Connect. maxBytes is the
// per-inference read cap (PayloadResponseByteLimit); <=0 uses
// DefaultRPCPayloadMaxBytes (64 MiB), matching HTTP GET. The Connect
// stub is the smallest rpcPayloadReadBuckets entry >=
// PayloadReadLimit(maxBytes) (32 / 64 / 256 / 512 MiB) — a transport
// backstop. After decode, prompt+response must still fit
// PayloadReadLimit(maxBytes) or the call is ResourceExhausted (dropped,
// not truncated), same as HTTP cappedReader.
func (c *RPCClient) GetPayload(ctx context.Context, req *rpcpb.GetPayloadRequest, maxBytes int64) (*rpcpb.GetPayloadResponse, error) {
	if !c.Uses(EndpointPayload) {
		return nil, fmt.Errorf("get payload: rpc endpoint not opted in")
	}
	// Signature must be the HTTP Authorization header value (base64 text)
	// as UTF-8 bytes, not raw ECDSA. The server adapter does
	// string(req.GetSignature()) then base64-decodes that string.
	readyCtx, cancelReady := context.WithTimeout(ctx, chatReadyTimeout)
	defer cancelReady()
	if err := c.WaitReady(readyCtx); err != nil {
		// The handshake budget expired while the caller is still running:
		// the executor never became ready. A closed local session returns
		// before that budget and stays a plain ErrPeerNotReady.
		if readyCtx.Err() != nil && ctx.Err() == nil {
			return nil, fmt.Errorf("get payload: %w", ErrPeerPayloadUnanswered)
		}
		return nil, fmt.Errorf("get payload: %w", err)
	}
	client, err := c.payloadClient(maxBytes)
	if err != nil {
		return nil, fmt.Errorf("get payload: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.QueryTimeout)
	defer cancel()
	var out *rpcpb.GetPayloadResponse
	err = c.rpcAttempt(ctx, rpcpbconnect.PayloadServiceGetPayloadProcedure, func() error {
		creq, err := tokenRequest(ctx, c, req)
		if err != nil {
			if errors.Is(err, ErrPeerNotReady) {
				return ErrPeerPayloadUnanswered
			}
			return err
		}
		resp, err := client.GetPayload(ctx, creq)
		if err != nil {
			return err
		}
		out = resp.Msg
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("get payload: %w", err)
	}
	limit := validation.PayloadReadLimit(maxBytes)
	if n := payloadDecodedSize(out); n > limit {
		return nil, fmt.Errorf("get payload: %w", errPayloadDecodedTooLarge(n, limit))
	}
	return out, nil
}

// cloneWithSigner keeps the PeerConn, Attach token, and endpoint set, and
// replaces HTTPClient.signer. signEnvelope uses the new key, so openSigned
// rejects a different address (PermissionDenied "envelope signer does not
// match handshake"). RepairProbe clones to s.host.Signer(); that works
// only when peerClients were stored host-signed (SetPeerClients). Do not
// Attach as the clone's signer here — a second PeerConn is a product
// feature no caller needs. ownsConn is false.
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
	out.retainDoor()
	return &out
}

func (c *RPCClient) retainDoor() {
	if c == nil || c.conn == nil || c.HTTPClient == nil {
		return
	}
	c.conn.addDoor(c.HTTPClient.escrowID)
}

var (
	_ gossip.PeerClient  = (*HTTPClient)(nil)
	_ gossip.PeerClient  = (*RPCClient)(nil)
	_ gossip.DiffFetcher = (*HTTPClient)(nil)
	_ gossip.DiffFetcher = (*RPCClient)(nil)
)
