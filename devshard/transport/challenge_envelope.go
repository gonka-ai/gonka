package transport

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"devshard/host"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/types"
)

var errChallengeEnvelopeSent = errors.New("challenge envelope already encoded")

// challengeBody is a ChallengeReceiptRequest whose diffs are stored wires.
// Each diff's txs is the nonce field followed by the stored blob, the same
// bytes DiffWireTxs builds. The encoding equals proto.Marshal of that request.
type challengeBody struct {
	inferenceID uint64
	payload     []byte
	hasPayload  bool
	wires       []types.DiffWire
}

func newChallengeBody(inferenceID uint64, payload *host.InferencePayload, wires []types.DiffWire) (challengeBody, error) {
	b := challengeBody{inferenceID: inferenceID, wires: wires}
	if p := PayloadJSONToProto(PayloadToJSON(payload)); p != nil {
		raw, err := proto.Marshal(p)
		if err != nil {
			return challengeBody{}, fmt.Errorf("marshal payload: %w", err)
		}
		b.payload, b.hasPayload = raw, true
	}
	return b, nil
}

func wireTxsSize(w types.DiffWire) int {
	return protowire.SizeTag(1) + protowire.SizeVarint(w.Nonce) + len(w.Txs)
}

func wireDiffSize(w types.DiffWire) int {
	n := protowire.SizeTag(2) + protowire.SizeBytes(wireTxsSize(w))
	if w.Nonce != 0 {
		n += protowire.SizeTag(1) + protowire.SizeVarint(w.Nonce)
	}
	if len(w.UserSig) > 0 {
		n += protowire.SizeTag(3) + protowire.SizeBytes(len(w.UserSig))
	}
	if len(w.PostStateRoot) > 0 {
		n += protowire.SizeTag(4) + protowire.SizeBytes(len(w.PostStateRoot))
	}
	return n
}

func (b challengeBody) size() int {
	n := 0
	if b.inferenceID != 0 {
		n += protowire.SizeTag(1) + protowire.SizeVarint(b.inferenceID)
	}
	if b.hasPayload {
		n += protowire.SizeTag(2) + protowire.SizeBytes(len(b.payload))
	}
	for _, w := range b.wires {
		n += protowire.SizeTag(3) + protowire.SizeBytes(wireDiffSize(w))
	}
	return n
}

// parts passes the body to emit in order. Framing bytes share one scratch
// buffer, so emit must consume each part before it returns. Stored bytes are
// passed as they are.
func (b challengeBody) parts(emit func([]byte)) {
	var scratch [64]byte
	h := scratch[:0]
	flush := func(data []byte) {
		emit(h)
		h = scratch[:0]
		if len(data) > 0 {
			emit(data)
		}
	}
	if b.inferenceID != 0 {
		h = protowire.AppendTag(h, 1, protowire.VarintType)
		h = protowire.AppendVarint(h, b.inferenceID)
	}
	if b.hasPayload {
		h = protowire.AppendTag(h, 2, protowire.BytesType)
		h = protowire.AppendVarint(h, uint64(len(b.payload)))
		flush(b.payload)
	}
	for _, w := range b.wires {
		h = protowire.AppendTag(h, 3, protowire.BytesType)
		h = protowire.AppendVarint(h, uint64(wireDiffSize(w)))
		if w.Nonce != 0 {
			h = protowire.AppendTag(h, 1, protowire.VarintType)
			h = protowire.AppendVarint(h, w.Nonce)
		}
		h = protowire.AppendTag(h, 2, protowire.BytesType)
		h = protowire.AppendVarint(h, uint64(wireTxsSize(w)))
		h = protowire.AppendTag(h, 1, protowire.VarintType)
		h = protowire.AppendVarint(h, w.Nonce)
		flush(w.Txs)
		if len(w.UserSig) > 0 {
			h = protowire.AppendTag(h, 3, protowire.BytesType)
			h = protowire.AppendVarint(h, uint64(len(w.UserSig)))
			flush(w.UserSig)
		}
		if len(w.PostStateRoot) > 0 {
			h = protowire.AppendTag(h, 4, protowire.BytesType)
			h = protowire.AppendVarint(h, uint64(len(w.PostStateRoot)))
			flush(w.PostStateRoot)
		}
	}
	if len(h) > 0 {
		emit(h)
	}
}

func (b challengeBody) appendTo(dst []byte) []byte {
	dst = slices.Grow(dst, b.size())
	b.parts(func(p []byte) { dst = append(dst, p...) })
	return dst
}

// challengeEnvelope is a SignedEnvelope around a challengeBody. challengeCodec
// writes it into Connect's send buffer, so the body is not built on its own.
// The wires are dropped once encoded; the envelope encodes once.
type challengeEnvelope struct {
	escrowID string
	body     challengeBody
	bodySize int
	sig      []byte
	ts       int64
	encoded  bool
}

// signatureMessage is signatureMessage(escrowID, body, ts) without the body bytes.
func (e *challengeEnvelope) signatureMessage() []byte {
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(e.ts))
	h := sha256.New()
	h.Write([]byte(e.escrowID))
	e.body.parts(func(p []byte) { h.Write(p) })
	h.Write(ts[:])
	return h.Sum(nil)
}

func (e *challengeEnvelope) size() int {
	n := 0
	if e.bodySize > 0 {
		n += protowire.SizeTag(1) + protowire.SizeBytes(e.bodySize)
	}
	if len(e.sig) > 0 {
		n += protowire.SizeTag(2) + protowire.SizeBytes(len(e.sig))
	}
	if e.ts != 0 {
		n += protowire.SizeTag(3) + protowire.SizeVarint(uint64(e.ts))
	}
	if e.escrowID != "" {
		n += protowire.SizeTag(4) + protowire.SizeBytes(len(e.escrowID))
	}
	return n
}

func (e *challengeEnvelope) appendTo(dst []byte) ([]byte, error) {
	if e.encoded {
		return nil, errChallengeEnvelopeSent
	}
	e.encoded = true
	dst = slices.Grow(dst, e.size())
	if e.bodySize > 0 {
		dst = protowire.AppendTag(dst, 1, protowire.BytesType)
		dst = protowire.AppendVarint(dst, uint64(e.bodySize))
		dst = e.body.appendTo(dst)
	}
	e.body.wires = nil
	if len(e.sig) > 0 {
		dst = protowire.AppendTag(dst, 2, protowire.BytesType)
		dst = protowire.AppendBytes(dst, e.sig)
	}
	if e.ts != 0 {
		dst = protowire.AppendTag(dst, 3, protowire.VarintType)
		dst = protowire.AppendVarint(dst, uint64(e.ts))
	}
	if e.escrowID != "" {
		dst = protowire.AppendTag(dst, 4, protowire.BytesType)
		dst = protowire.AppendString(dst, e.escrowID)
	}
	return dst, nil
}

// challengeCodec is the proto codec for challengeEnvelope requests. Other
// messages, including the response, use proto as usual.
type challengeCodec struct{}

func (challengeCodec) Name() string { return "proto" }

func (c challengeCodec) Marshal(msg any) ([]byte, error) {
	return c.MarshalAppend(nil, msg)
}

func (challengeCodec) MarshalAppend(dst []byte, msg any) ([]byte, error) {
	switch m := msg.(type) {
	case *challengeEnvelope:
		return m.appendTo(dst)
	case proto.Message:
		return proto.MarshalOptions{}.MarshalAppend(dst, m)
	default:
		return nil, fmt.Errorf("challenge codec: unexpected %T", msg)
	}
}

func (challengeCodec) Unmarshal(data []byte, msg any) error {
	m, ok := msg.(proto.Message)
	if !ok {
		return fmt.Errorf("challenge codec: unexpected %T", msg)
	}
	return proto.Unmarshal(data, m)
}

type challengeClient = *connect.Client[challengeEnvelope, rpcpb.ChallengeReceiptResponse]

func newChallengeClient(httpClient connect.HTTPClient, base string, opts []connect.ClientOption) challengeClient {
	method := rpcpb.File_devshard_transport_v1_session_proto.Services().ByName("SessionService").Methods().ByName("ChallengeReceipt")
	return connect.NewClient[challengeEnvelope, rpcpb.ChallengeReceiptResponse](
		httpClient,
		strings.TrimRight(base, "/")+rpcpbconnect.SessionServiceChallengeReceiptProcedure,
		connect.WithSchema(method),
		connect.WithClientOptions(append(slices.Clip(opts), connect.WithCodec(challengeCodec{}))...),
	)
}

// challengePage hands the page to the first send attempt. With a reload, a
// later attempt reads the page again, so only the request in flight holds it.
type challengePage struct {
	wires  []types.DiffWire
	reload host.WireReload
	taken  bool
}

func (p *challengePage) take() ([]types.DiffWire, error) {
	if p.taken && p.reload != nil {
		return p.reload()
	}
	p.taken = true
	wires := p.wires
	if p.reload != nil {
		p.wires = nil
	}
	return wires, nil
}

// sendChallenge sends body with the page as its diffs. Each attempt signs the
// page and encodes it into Connect's send buffer.
func (c *RPCClient) sendChallenge(ctx context.Context, client challengeClient, body challengeBody, page *challengePage) (*rpcpb.ChallengeReceiptResponse, error) {
	var out *rpcpb.ChallengeReceiptResponse
	err := c.rpcAttempt(ctx, rpcpbconnect.SessionServiceChallengeReceiptProcedure, func() error {
		creq, err := tokenRequest(ctx, c, &challengeEnvelope{})
		if err != nil {
			return err
		}
		wires, err := page.take()
		if err != nil {
			return err
		}
		if err := c.sealChallenge(creq.Msg, body, wires); err != nil {
			return err
		}
		resp, err := client.CallUnary(ctx, creq)
		if err != nil {
			return err
		}
		out = resp.Msg
		return nil
	})
	return out, err
}

func (c *RPCClient) sealChallenge(env *challengeEnvelope, body challengeBody, wires []types.DiffWire) error {
	if c.signer == nil {
		return fmt.Errorf("rpc client: signer is required")
	}
	body.wires = wires
	env.escrowID = c.escrowID
	env.body = body
	env.bodySize = body.size()
	env.ts = time.Now().Unix()
	sig, err := c.signer.Sign(env.signatureMessage())
	if err != nil {
		return err
	}
	env.sig = sig
	return nil
}

func (c *RPCClient) challengeLargeClient() challengeClient {
	if c.grpcOn() && c.challengeLargeGRPC != nil {
		return c.challengeLargeGRPC
	}
	return c.challengeLarge
}
