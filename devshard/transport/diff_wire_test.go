package transport

import (
	"bytes"
	"context"
	"runtime"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/transport/rpcpb"
	"devshard/types"
)

func TestDiffWireTxsMatchesMarshal(t *testing.T) {
	cases := []struct {
		name  string
		nonce uint64
		txs   []*types.DevshardTx
	}{
		{name: "empty", nonce: 1},
		{name: "start", nonce: 2, txs: []*types.DevshardTx{testutil.StartTx(1)}},
		{name: "many", nonce: 3, txs: []*types.DevshardTx{
			testutil.StartTxVersioned(1, "v6"),
			{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{InferenceId: 2, Model: "llama"}}},
			{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{InferenceId: 3, Model: "qwen"}}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored, err := proto.Marshal(&types.DiffContent{Txs: tc.txs})
			require.NoError(t, err)
			want, err := marshalDiffWireTxs(types.Diff{Nonce: tc.nonce, Txs: tc.txs})
			require.NoError(t, err)
			require.Equal(t, want, DiffWireTxs(tc.nonce, stored))
		})
	}
}

func TestDecodeDiffWireUsesOuterNonce(t *testing.T) {
	user := testutil.MustGenerateKey(t)
	txs := []*types.DevshardTx{testutil.StartTxVersioned(1, "v6")}
	root := bytes.Repeat([]byte{3}, 32)
	const nonce = uint64(4)
	diff := testutil.SignDiffWithRoot(t, user, "escrow-1", nonce, txs, root)
	stored, err := proto.Marshal(&types.DiffContent{Txs: txs})
	require.NoError(t, err)

	decoded, err := types.DecodeDiffWire(types.DiffWire{
		Nonce:         nonce,
		Txs:           DiffWireTxs(99, stored),
		UserSig:       diff.UserSig,
		PostStateRoot: diff.PostStateRoot,
	})
	require.NoError(t, err)
	require.Equal(t, nonce, decoded.Nonce)
	require.NoError(t, state.VerifyDiffUserSig(signing.NewSecp256k1Verifier(), user.Address(), "escrow-1", decoded))
}

func TestDecodeDiffWireCopiesBytes(t *testing.T) {
	txs := []*types.DevshardTx{testutil.StartTx(1)}
	stored, err := proto.Marshal(&types.DiffContent{Nonce: 9, Txs: txs})
	require.NoError(t, err)
	buf := append([]byte(nil), stored...)
	sig := []byte("user-sig")
	root := bytes.Repeat([]byte{5}, 32)
	decoded, err := types.DecodeDiffWire(types.DiffWire{
		Nonce:         4,
		Txs:           buf,
		UserSig:       sig,
		PostStateRoot: root,
	})
	require.NoError(t, err)
	buf[0] ^= 0xff
	sig[0] ^= 0xff
	root[0] ^= 0xff

	fresh, err := proto.Marshal(&types.DiffContent{Txs: txs})
	require.NoError(t, err)
	again, err := types.DecodeDiffWire(types.DiffWire{
		Nonce:         4,
		Txs:           fresh,
		UserSig:       []byte("user-sig"),
		PostStateRoot: bytes.Repeat([]byte{5}, 32),
	})
	require.NoError(t, err)
	require.Equal(t, again.Nonce, decoded.Nonce)
	require.True(t, proto.Equal(again.Txs[0], decoded.Txs[0]))
	require.Equal(t, again.UserSig, decoded.UserSig)
	require.Equal(t, again.PostStateRoot, decoded.PostStateRoot)
}

func TestChallengeCatchUpAppliesTheSameRoot(t *testing.T) {
	signers := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(signers)
	config := testutil.DefaultConfig(len(signers))
	newHost := func() *host.Host {
		t.Helper()
		sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), signing.NewSecp256k1Verifier(),
			testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000))
		require.NoError(t, err)
		h, err := host.NewHost(sm, signers[0], stub.NewInferenceEngine(), "escrow-1", group, nil)
		require.NoError(t, err)
		return h
	}
	preview, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000))
	require.NoError(t, err)
	txs := []*types.DevshardTx{testutil.StartTx(1)}
	root, err := preview.ApplyLocal(1, txs)
	require.NoError(t, err)
	diff := testutil.SignDiffWithRoot(t, user, "escrow-1", 1, txs, root)
	stored, err := proto.Marshal(&types.DiffContent{Txs: diff.Txs})
	require.NoError(t, err)

	body, err := ChallengeCatchUpBody(1, []types.DiffWire{{
		Nonce:         diff.Nonce,
		Txs:           stored,
		UserSig:       diff.UserSig,
		PostStateRoot: diff.PostStateRoot,
	}})
	require.NoError(t, err)
	var req rpcpb.ChallengeReceiptRequest
	require.NoError(t, proto.Unmarshal(body, &req))
	decoded, err := DiffsFromJSON(diffsJSONFromProto(req.GetDiffs()))
	require.NoError(t, err)
	require.Len(t, decoded, 1)
	require.True(t, proto.Equal(diff.Txs[0], decoded[0].Txs[0]))

	verifier := signing.NewSecp256k1Verifier()
	require.NoError(t, state.VerifyDiffUserSig(verifier, user.Address(), "escrow-1", diff))
	require.NoError(t, state.VerifyDiffUserSig(verifier, user.Address(), "escrow-1", decoded[0]))

	ctx := context.Background()
	original := newHost()
	fromWire := newHost()
	_, _, err = original.ChallengeReceipt(ctx, 1, nil, []types.Diff{diff})
	require.NoError(t, err)
	_, _, err = fromWire.ChallengeReceipt(ctx, 1, nil, decoded)
	require.NoError(t, err)
	rootOriginal, err := original.StateRoot()
	require.NoError(t, err)
	rootWire, err := fromWire.StateRoot()
	require.NoError(t, err)
	require.Equal(t, root, rootOriginal)
	require.Equal(t, rootOriginal, rootWire)
}

func TestChallengeCatchUpBodyAllocsIgnoreTxCount(t *testing.T) {
	const total = 4 << 20
	oneTx := []*types.DevshardTx{{
		Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			PromptHash: bytes.Repeat([]byte{0xab}, total),
		}},
	}}
	const n = 200
	chunk := total / n
	manyTx := make([]*types.DevshardTx, n)
	for i := range manyTx {
		manyTx[i] = &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			PromptHash: bytes.Repeat([]byte{byte(i)}, chunk),
		}}}
	}
	oneStored, err := proto.Marshal(&types.DiffContent{Txs: oneTx})
	require.NoError(t, err)
	manyStored, err := proto.Marshal(&types.DiffContent{Txs: manyTx})
	require.NoError(t, err)
	sig := bytes.Repeat([]byte{1}, 64)
	root := bytes.Repeat([]byte{2}, 32)
	one := types.DiffWire{Nonce: 1, Txs: oneStored, UserSig: sig, PostStateRoot: root}
	many := types.DiffWire{Nonce: 1, Txs: manyStored, UserSig: sig, PostStateRoot: root}

	oneAllocs := testing.AllocsPerRun(20, func() {
		if _, err := ChallengeCatchUpBody(1, []types.DiffWire{one}); err != nil {
			panic(err)
		}
	})
	manyAllocs := testing.AllocsPerRun(20, func() {
		if _, err := ChallengeCatchUpBody(1, []types.DiffWire{many}); err != nil {
			panic(err)
		}
	})
	require.LessOrEqual(t, manyAllocs, oneAllocs+2)
}

func TestChallengeReceiptWireBodyKeepsStoredBytes(t *testing.T) {
	stored := bytes.Repeat([]byte{0xab}, 64)
	sig := bytes.Repeat([]byte{1}, 64)
	root := bytes.Repeat([]byte{2}, 32)
	payload := &host.InferencePayload{Prompt: []byte("prompt"), Model: "llama"}
	wire := types.DiffWire{Nonce: 7, Txs: stored, UserSig: sig, PostStateRoot: root}

	body, err := ChallengeReceiptWireBody(3, payload, []types.DiffWire{wire})
	require.NoError(t, err)
	var req rpcpb.ChallengeReceiptRequest
	require.NoError(t, proto.Unmarshal(body, &req))
	require.Equal(t, uint64(3), req.GetInferenceId())
	require.Equal(t, payload.Prompt, req.GetPayload().GetPrompt())
	require.Len(t, req.GetDiffs(), 1)
	require.Equal(t, DiffWireTxs(wire.Nonce, stored), req.GetDiffs()[0].GetTxs())
	require.Equal(t, sig, req.GetDiffs()[0].GetUserSig())

	const total = 4 << 20
	oneTx := []*types.DevshardTx{{
		Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			PromptHash: bytes.Repeat([]byte{0xab}, total),
		}},
	}}
	const n = 200
	chunk := total / n
	manyTx := make([]*types.DevshardTx, n)
	for i := range manyTx {
		manyTx[i] = &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			PromptHash: bytes.Repeat([]byte{byte(i)}, chunk),
		}}}
	}
	oneStored, err := proto.Marshal(&types.DiffContent{Txs: oneTx})
	require.NoError(t, err)
	manyStored, err := proto.Marshal(&types.DiffContent{Txs: manyTx})
	require.NoError(t, err)
	one := types.DiffWire{Nonce: 1, Txs: oneStored, UserSig: sig, PostStateRoot: root}
	many := types.DiffWire{Nonce: 1, Txs: manyStored, UserSig: sig, PostStateRoot: root}
	oneAllocs := testing.AllocsPerRun(20, func() {
		if _, err := ChallengeReceiptWireBody(1, payload, []types.DiffWire{one}); err != nil {
			panic(err)
		}
	})
	manyAllocs := testing.AllocsPerRun(20, func() {
		if _, err := ChallengeReceiptWireBody(1, payload, []types.DiffWire{many}); err != nil {
			panic(err)
		}
	})
	require.LessOrEqual(t, manyAllocs, oneAllocs+2)
}

func TestChallengeEnvelopeMatchesProtoMarshal(t *testing.T) {
	sig := bytes.Repeat([]byte{1}, 65)
	root := bytes.Repeat([]byte{2}, 32)
	wires := []types.DiffWire{
		{Nonce: 0, Txs: []byte{0x12, 0x00}},
		{Nonce: 7, Txs: bytes.Repeat([]byte{0xab}, 300), UserSig: sig, PostStateRoot: root},
		{Nonce: 1 << 40, UserSig: sig},
	}
	prompt := &host.InferencePayload{Prompt: bytes.Repeat([]byte("p"), 200), Model: "llama", InputLength: 5, MaxTokens: 10, StartedAt: 1000}
	cases := []struct {
		name        string
		inferenceID uint64
		payload     *host.InferencePayload
		wires       []types.DiffWire
	}{
		{name: "catch-up page", inferenceID: 9, wires: wires},
		{name: "payload and page", inferenceID: 3, payload: prompt, wires: wires[1:2]},
		{name: "empty payload", payload: &host.InferencePayload{}},
		{name: "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := &rpcpb.ChallengeReceiptRequest{
				InferenceId: tc.inferenceID,
				Payload:     PayloadJSONToProto(PayloadToJSON(tc.payload)),
			}
			for _, w := range tc.wires {
				want.Diffs = append(want.Diffs, &rpcpb.Diff{
					Nonce:         w.Nonce,
					Txs:           DiffWireTxs(w.Nonce, w.Txs),
					UserSig:       w.UserSig,
					PostStateRoot: w.PostStateRoot,
				})
			}
			wantBody, err := proto.Marshal(want)
			require.NoError(t, err)

			body, err := newChallengeBody(tc.inferenceID, tc.payload, tc.wires)
			require.NoError(t, err)
			require.Equal(t, len(wantBody), body.size())
			require.True(t, bytes.Equal(wantBody, body.appendTo(nil)), "body bytes")

			env := &challengeEnvelope{escrowID: "escrow-1", body: body, bodySize: body.size(), sig: sig, ts: 1_700_000_000}
			require.Equal(t, signatureMessage("escrow-1", wantBody, env.ts), env.signatureMessage())
			wantEnv, err := proto.Marshal(&rpcpb.SignedEnvelope{Payload: wantBody, Signature: sig, Timestamp: env.ts, EscrowId: "escrow-1"})
			require.NoError(t, err)
			require.Equal(t, len(wantEnv), env.size())
			got, err := challengeCodec{}.MarshalAppend(nil, env)
			require.NoError(t, err)
			require.True(t, bytes.Equal(wantEnv, got), "envelope bytes")
			require.Nil(t, env.body.wires, "the encoded envelope drops the page")
			_, err = challengeCodec{}.MarshalAppend(nil, env)
			require.ErrorIs(t, err, errChallengeEnvelopeSent)
		})
	}
}

func TestChallengeEnvelopeEncodesIntoTheSendBuffer(t *testing.T) {
	const n, each = 100, 40 << 10
	sig := bytes.Repeat([]byte{1}, 65)
	root := bytes.Repeat([]byte{2}, 32)
	page := make([]types.DiffWire, n)
	for i := range page {
		page[i] = types.DiffWire{Nonce: uint64(i + 1), Txs: bytes.Repeat([]byte{byte(i)}, each), UserSig: sig, PostStateRoot: root}
	}
	body, err := newChallengeBody(1, nil, page)
	require.NoError(t, err)
	env := &challengeEnvelope{escrowID: "escrow-1", body: body, bodySize: body.size(), sig: sig, ts: 1}
	dst := make([]byte, 0, env.size())

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_ = env.signatureMessage()
	out, err := challengeCodec{}.MarshalAppend(dst, env)
	runtime.ReadMemStats(&after)
	require.NoError(t, err)
	require.Equal(t, env.size(), len(out))
	require.Same(t, unsafe.SliceData(dst[:1]), unsafe.SliceData(out), "the page is written into the buffer it was given")
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(64<<10), "signing and encoding a %d KiB page", n*each>>10)
}
