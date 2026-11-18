package transport

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/types"
)

func heightAckFanBody(t *testing.T, hashHexChars, acks int) []byte {
	t.Helper()
	txs := make([]*types.DevshardTx, 0, acks)
	for i := 0; i < acks; i++ {
		txs = append(txs, &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: &types.MsgHeightAck{
			RefNonce: 1, ObservedHeight: 1, ObservedBlockHash: []byte{1},
		}}})
	}
	raw, err := proto.Marshal(&types.DiffContent{Nonce: 1, Txs: txs})
	require.NoError(t, err)
	hs := &heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         1_000_000,
		MainnetBlockHashHex:   strings.Repeat("a", hashHexChars),
		TimestampUnixMs:       time.Now().UnixMilli(),
		Direction:             "response",
		OriginatorSenderID:    "gonka1qypqxpq9qcrsszg2pvxq6rs0zqg3yyc5lzv7xu",
		OriginatorTimestampMs: time.Now().UnixMilli(),
	}
	body, err := MarshalWrappedInferenceRequest(1, hs, InferenceRequest{Nonce: 1, Diffs: []DiffJSON{{Nonce: 1, Txs: raw}}})
	require.NoError(t, err)
	return body
}

func postHeightAckFan(t *testing.T, hashHexChars, acks int) (int, string, int, uint64) {
	t.Helper()
	src := heightsync.NewPeerTipOracleSource(NewHeightSyncPeerTips(), time.Minute)
	env := setupServerEnv(t, WithHeightSync(heightsync.MustNewAnchorScheduler(8, 4, src), nil))
	body := heightAckFanBody(t, hashHexChars, acks)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	rec := env.doPost(t, "/devshard/v2/sessions/escrow-1/chat/completions", body)
	runtime.ReadMemStats(&after)
	return rec.Code, rec.Body.String(), env.server.heightSyncMarks.Len(), after.TotalAlloc - before.TotalAlloc
}

func TestChat_OversizedHeightSyncSectionRejectedBeforeMarks(t *testing.T) {
	code, body, marks, alloc := postHeightAckFan(t, 1<<20, 1000)
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, body, "mainnet_block_hash_hex")
	require.Zero(t, marks)
	require.Less(t, alloc, uint64(64<<20))
}

func TestChat_OversizedOriginatorSenderIDRejected(t *testing.T) {
	body := heightAckFanBody(t, 64, 1)
	var env types.InferenceRequestEnvelope
	require.NoError(t, proto.Unmarshal(body, &env))
	env.HeightSync.OriginatorSenderId = strings.Repeat("g", 1<<20)
	raw, err := proto.Marshal(&env)
	require.NoError(t, err)
	_, err = UnwrapInferenceRequestBody(raw)
	require.ErrorIs(t, err, heightsync.ErrSectionOversized)
}

func TestChat_HonestSectionWithAckFanKeepsMarksBounded(t *testing.T) {
	code, body, marks, alloc := postHeightAckFan(t, 64, 20000)
	require.Equal(t, http.StatusInternalServerError, code)
	require.Contains(t, body, "invalid user signature")
	require.Positive(t, marks)
	require.LessOrEqual(t, marks, heightsync.MaxEnvelopeBindingMarks)
	require.Less(t, alloc, uint64(256<<20))
}

func TestUnwrapInferenceResponseBody_OversizedSectionRejected(t *testing.T) {
	raw, err := proto.Marshal(&types.InferenceResponseEnvelope{
		SchemaVersion:         1,
		InferenceResponseJson: []byte(`{}`),
		HeightSync: &types.InferenceHeightSyncSection{
			ProofType:           types.InferenceHeightSyncProofType_INFERENCE_HEIGHT_SYNC_PROOF_TYPE_HEIGHT_ANCHOR_V1,
			MainnetHeight:       10,
			MainnetBlockHashHex: strings.Repeat("a", 1<<20),
			Response:            true,
		},
	})
	require.NoError(t, err)
	_, err = UnwrapInferenceResponseBody(raw)
	require.ErrorIs(t, err, heightsync.ErrSectionOversized)
}

func TestChat_HonestBoundedSectionStillServed(t *testing.T) {
	src := heightsync.NewPeerTipOracleSource(NewHeightSyncPeerTips(), time.Minute)
	env := setupServerEnv(t, WithHeightSync(heightsync.MustNewAnchorScheduler(8, 4, src), nil))
	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	dj, err := DiffToJSON(diff)
	require.NoError(t, err)
	ir := InferenceRequest{
		Diffs:   []DiffJSON{dj},
		Nonce:   1,
		Payload: &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000},
	}
	hs := &heightsync.HeightSyncSection{
		ChainID:             "gonka-mainnet",
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       11,
		MainnetBlockHashHex: "0x" + hex.EncodeToString(bytes.Repeat([]byte{0xbb}, heightsync.MaxObservedBlockHashBytes)),
		TimestampUnixMs:     time.Now().UnixMilli(),
		Direction:           "request",
	}
	body, err := MarshalWrappedInferenceRequest(CurrentInferenceEnvelopeSchemaVersion, hs, ir)
	require.NoError(t, err)
	rec := env.doPostContentType(t, "/devshard/v2/sessions/escrow-1/chat/completions", "application/x-protobuf", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
