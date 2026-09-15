package transport

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"common/validation"
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
	// sessionQuery is a second SessionServiceClient with the 10 MiB catch-up
	// read cap (DefaultMaxBodySize). Connect applies WithReadMaxBytes per
	// client, not per RPC, so GetDiffs / GetMempool cannot share the 16 KiB
	// session client.
	sessionQuery rpcpbconnect.SessionServiceClient
	// sessionLarge is a third SessionServiceClient with the 10 MiB dispute
	// read cap (VerifyTimeout, VerifyErrorMiss, ChallengeReceipt responses).
	sessionLarge rpcpbconnect.SessionServiceClient
	// sessionChat is SessionService.Chat: 10 MiB envelope, no per-frame gzip.
	sessionChat rpcpbconnect.SessionServiceClient
	gossip       rpcpbconnect.GossipServiceClient
	// payload uses DefaultRPCPayloadMaxBytes (64 MiB) when the caller
	// did not pass a per-inference limit. GetPayload builds a tighter or
	// larger client from PayloadReadLimit.
	payload rpcpbconnect.PayloadServiceClient
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
	}
	if conn != nil && httpClient != nil {
		base := conn.cfg.connectBase(httpClient.escrowID)
		opts := connectClientOptions(conn.cfg.ReadMaxBytes)
		c.session = rpcpbconnect.NewSessionServiceClient(conn.http, base, opts...)
		c.sessionQuery = rpcpbconnect.NewSessionServiceClient(conn.http, base, connectClientOptions(DefaultRPCQueryReadMaxBytes)...)
		c.sessionLarge = rpcpbconnect.NewSessionServiceClient(conn.http, base, connectClientOptions(DefaultRPCLargeReadMaxBytes)...)
		c.sessionChat = rpcpbconnect.NewSessionServiceClient(conn.http, base, chatClientOptions()...)
		c.gossip = rpcpbconnect.NewGossipServiceClient(conn.http, base, opts...)
		c.payload = rpcpbconnect.NewPayloadServiceClient(conn.http, base, connectClientOptions(DefaultRPCPayloadMaxBytes)...)
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
// ErrPeerNotReady is not retryable; callers that issue an RPC before the
// first handshake must wait here instead of treating the error as a miss.
func (c *RPCClient) WaitReady(ctx context.Context) error {
	if c == nil || c.conn == nil {
		return ErrPeerNotReady
	}
	if c.conn.Ready() {
		return nil
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if c.conn.Ready() {
			return nil
		}
		select {
		case <-ctx.Done():
			if c.conn.Ready() {
				return nil
			}
			return fmt.Errorf("%w: %v", ErrPeerNotReady, ctx.Err())
		case <-ticker.C:
		}
	}
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
		if ra := connectRetryAfter(err); ra > 0 {
			if ra > remaining {
				return err
			}
			sleep = ra
		} else if sleep > remaining {
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
	return rpcRetry(ctx, func() error {
		return c.withPeerBudget(ctx, procedure, fn)
	})
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
		connect.WithSendGzip(),
	}
}

// chatClientOptions gzips the request envelope (prompt + catch-up diffs) like
// the unary clients and HTTP Send. gzip must stay registered for WithSendGzip
// to resolve; the handler never compresses ChatFrames, so response chunks are
// still one application gzip stream and are not compressed twice.
func chatClientOptions() []connect.ClientOption {
	return []connect.ClientOption{
		connect.WithReadMaxBytes(int(DefaultMaxBodySize)),
		connect.WithSendGzip(),
	}
}

func (c *RPCClient) GetSignatures(ctx context.Context, nonce uint64) (map[uint32][]byte, error) {
	if !c.Uses(EndpointSignatures) {
		return c.HTTPClient.GetSignatures(ctx, nonce)
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.QueryTimeout)
	defer cancel()
	var out map[uint32][]byte
	err := c.rpcAttempt(ctx, rpcpbconnect.SessionServiceGetSignaturesProcedure, func() error {
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
	ctx, cancel := context.WithTimeout(ctx, c.config.QueryTimeout)
	defer cancel()
	var diffs []types.Diff
	err := c.rpcAttempt(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, func() error {
		req, err := tokenRequest(c, &rpcpb.GetDiffsRequest{From: from, To: to})
		if err != nil {
			return err
		}
		resp, err := c.sessionQuery.GetDiffs(ctx, req)
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

func (c *RPCClient) GetMempool(ctx context.Context) ([]*types.DevshardTx, error) {
	if !c.Uses(EndpointMempool) {
		return c.HTTPClient.GetMempool(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.QueryTimeout)
	defer cancel()
	var txs []*types.DevshardTx
	err := c.rpcAttempt(ctx, rpcpbconnect.SessionServiceGetMempoolProcedure, func() error {
		req, err := tokenRequest(c, &rpcpb.GetMempoolRequest{})
		if err != nil {
			return err
		}
		resp, err := c.sessionQuery.GetMempool(ctx, req)
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
	return c.gossipSigned(ctx, c.config.GossipTimeout, rpcpbconnect.GossipServiceTxsProcedure, payload, func(ctx context.Context, req *connect.Request[rpcpb.SignedEnvelope]) error {
		_, err := c.gossip.Txs(ctx, req)
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
	if c == nil || c.heightSyncPeerTips == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.heightSeedTimeout())
	defer cancel()
	err = c.withPeerBudget(ctx, rpcpbconnect.SessionServiceSeedHeightSyncProcedure, func() error {
		env, err := c.signEnvelope(nil)
		if err != nil {
			return err
		}
		req, err := tokenRequest(c, env)
		if err != nil {
			return err
		}
		resp, err := c.session.SeedHeightSync(ctx, req)
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
		creq, err := tokenRequest(c, env)
		if err != nil {
			return err
		}
		resp, err := c.session.RepairHeightSync(ctx, creq)
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
		creq, err := tokenRequest(c, env)
		if err != nil {
			return err
		}
		resp, err := c.sessionLarge.VerifyTimeout(ctx, creq)
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
		creq, err := tokenRequest(c, env)
		if err != nil {
			return err
		}
		resp, err := c.sessionLarge.VerifyErrorMiss(ctx, creq)
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
	djList := make([]DiffJSON, len(diffs))
	for i, d := range diffs {
		dj, err := DiffToJSON(d)
		if err != nil {
			return nil, nil, fmt.Errorf("encode diff %d: %w", i, err)
		}
		djList[i] = dj
	}
	inner := ChallengeReceiptRequestToProto(ChallengeReceiptRequest{
		InferenceID: inferenceID,
		Payload:     PayloadToJSON(payload),
		Diffs:       djList,
	})
	raw, err := proto.Marshal(inner)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.VerifyTimeout)
	defer cancel()
	var receipt []byte
	var mempool []*types.DevshardTx
	err = c.rpcAttempt(ctx, rpcpbconnect.SessionServiceChallengeReceiptProcedure, func() error {
		env, err := c.signEnvelope(raw)
		if err != nil {
			return err
		}
		creq, err := tokenRequest(c, env)
		if err != nil {
			return err
		}
		resp, err := c.sessionLarge.ChallengeReceipt(ctx, creq)
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
		Payload:     PayloadToJSON(payload),
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

// payloadClient is the Connect GetPayload client for this call's read cap.
// Connect applies WithReadMaxBytes per client, not per RPC, so a
// per-inference limit that is not the 64 MiB default needs its own client.
// PayloadReadLimit is always positive, so connectClientOptions will not
// fall back to the 16 KiB handshake cap.
func (c *RPCClient) payloadClient(maxBytes int64) (rpcpbconnect.PayloadServiceClient, error) {
	limit := int(validation.PayloadReadLimit(maxBytes))
	if limit == DefaultRPCPayloadMaxBytes && c.payload != nil {
		return c.payload, nil
	}
	if c.conn == nil || c.HTTPClient == nil {
		return nil, fmt.Errorf("no peer connection")
	}
	base := c.conn.cfg.connectBase(c.HTTPClient.escrowID)
	return rpcpbconnect.NewPayloadServiceClient(c.conn.http, base, connectClientOptions(limit)...), nil
}

// GetPayload fetches inference payloads over Connect. maxBytes is the
// per-inference read cap (PayloadResponseByteLimit); <=0 uses
// DefaultRPCPayloadMaxBytes (64 MiB), matching HTTP GET. The cap is
// Connect WithReadMaxBytes(PayloadReadLimit(maxBytes)).
func (c *RPCClient) GetPayload(ctx context.Context, req *rpcpb.GetPayloadRequest, maxBytes int64) (*rpcpb.GetPayloadResponse, error) {
	if !c.Uses(EndpointPayload) {
		return nil, fmt.Errorf("get payload: rpc endpoint not opted in")
	}
	// Signature must be the HTTP Authorization header value (base64 text)
	// as UTF-8 bytes, not raw ECDSA. The server adapter does
	// string(req.GetSignature()) then base64-decodes that string.
	if err := c.WaitReady(ctx); err != nil {
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
		creq, err := tokenRequest(c, req)
		if err != nil {
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
	return &out
}

var (
	_ gossip.PeerClient  = (*HTTPClient)(nil)
	_ gossip.PeerClient  = (*RPCClient)(nil)
	_ gossip.DiffFetcher = (*HTTPClient)(nil)
	_ gossip.DiffFetcher = (*RPCClient)(nil)
)
