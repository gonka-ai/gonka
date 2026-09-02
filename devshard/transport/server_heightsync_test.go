package transport

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks"
	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/logging"
	"devshard/signing"
	"devshard/types"
)

func TestServer_Inference_HeightSync_OutboundAnchor(t *testing.T) {
	or := &heightSyncTestOracle{hdr: &blocks.Header{
		Height:    77,
		ChainID:   "chain-x",
		BlockHash: []byte{0xab, 0xcd},
	}}
	sched := heightsync.MustNewAnchorSchedulerFromOracle(10, 1, or)

	env := setupServerEnv(t, WithHeightSync(sched, or))

	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	dj, err := DiffToJSON(diff)
	require.NoError(t, err)
	ir := InferenceRequest{
		Diffs:   []DiffJSON{dj},
		Nonce:   1,
		Payload: &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000},
	}
	body, err := json.Marshal(ir)
	require.NoError(t, err)

	rec := env.doPost(t, "/devshard/v2/sessions/escrow-1/chat/completions", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var hs heightsync.HeightSyncSection
	foundHS := false
	lines := strings.Split(rec.Body.String(), "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &envelope); err != nil {
			continue
		}
		if raw, ok := envelope["height_sync"]; ok {
			require.NoError(t, json.Unmarshal(raw, &hs))
			foundHS = true
			break
		}
	}
	require.True(t, foundHS, "expected height_sync on first SSE receipt")
	localAddr := env.hostSigner.Address()
	require.Equal(t, heightsync.AnchorProofType, hs.ProofType)
	require.Equal(t, int64(77), hs.MainnetHeight)
	require.Equal(t, "response", hs.Direction)
	require.NotEmpty(t, hs.SenderSignature)
	require.Equal(t, localAddr, hs.OriginatorSenderID)
	require.NoError(t, heightsync.VerifyOrigin(signing.NewSecp256k1Verifier(), &hs, hs.SenderSignature))

	ring := env.server.HeightSyncAuditRing()
	require.NotNil(t, ring)
	var sawResponse bool
	for _, a := range ring.List(localAddr) {
		if a.Direction == "response" && a.MainnetHeight == 77 {
			sawResponse = true
			break
		}
	}
	require.True(t, sawResponse, "expected outbound anchor in audit ring")
	for _, a := range ring.List(localAddr) {
		if a.Direction == "response" && a.MainnetHeight == 77 {
			require.Equal(t, heightsync.TrustOracle, a.Trust)
			break
		}
	}
}

func sseFirstReceiptHasHeightSync(body string) bool {
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &envelope); err != nil {
			continue
		}
		if _, ok := envelope["devshard_receipt"]; !ok {
			continue
		}
		_, has := envelope["height_sync"]
		return has
	}
	return false
}

func txKindPriority(tx *types.DevshardTx) int {
	switch tx.GetTx().(type) {
	case *types.DevshardTx_ForceHeightSyncTurn:
		return -1
	case *types.DevshardTx_ConfirmStart:
		return 0
	case *types.DevshardTx_FinishInference:
		return 1
	default:
		return 2
	}
}

func pipelineInferenceTxs(mempool []*types.DevshardTx, extra ...*types.DevshardTx) []*types.DevshardTx {
	txs := append([]*types.DevshardTx(nil), mempool...)
	txs = append(txs, extra...)
	sort.SliceStable(txs, func(i, j int) bool {
		return txKindPriority(txs[i]) < txKindPriority(txs[j])
	})
	return txs
}

func warnsContain(warns []string, substr string) bool {
	for _, w := range warns {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// TestServer_Inference_HeightSync_ForceAnchor_OnInferenceRequest covers spec §9
// forced turns on the host side: InferenceRequest.force_height_sync_anchor
// drives DecideHints.ForceAnchor on the outbound receipt so the host emits
// Anchor even when responseNonce falls outside the sync-turn cadence.
func TestServer_Inference_HeightSync_ForceAnchor_OnInferenceRequest(t *testing.T) {
	or := &heightSyncTestOracle{hdr: &blocks.Header{
		Height:    77,
		ChainID:   "chain-x",
		BlockHash: []byte{0xab, 0xcd},
	}}
	sched := heightsync.MustNewAnchorSchedulerFromOracle(10, 1, or)
	env := setupServerEnv(t, WithHeightSync(sched, or))

	payload := &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}

	diff1 := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	dj1, err := DiffToJSON(diff1)
	require.NoError(t, err)
	ir1 := InferenceRequest{Diffs: []DiffJSON{dj1}, Nonce: 1, Payload: payload}
	body1, err := json.Marshal(ir1)
	require.NoError(t, err)
	rec1 := env.doPost(t, "/devshard/v2/sessions/escrow-1/chat/completions", body1)
	require.Equal(t, http.StatusOK, rec1.Code, rec1.Body.String())
	require.True(t, sseFirstReceiptHasHeightSync(rec1.Body.String()), "responseNonce=1 is inside initial sync turn (slots=1)")

	txs2 := pipelineInferenceTxs(env.server.Host().MempoolTxs(), testutil.StartTx(2))
	diff2 := testutil.SignDiff(t, env.userSigner, "escrow-1", 2, txs2)
	dj2, err := DiffToJSON(diff2)
	require.NoError(t, err)
	ir2 := InferenceRequest{Diffs: []DiffJSON{dj2}, Nonce: 2, Payload: payload}
	body2, err := json.Marshal(ir2)
	require.NoError(t, err)
	rec2 := env.doPost(t, "/devshard/v2/sessions/escrow-1/chat/completions", body2)
	require.Equal(t, http.StatusOK, rec2.Code, rec2.Body.String())
	require.False(t, sseFirstReceiptHasHeightSync(rec2.Body.String()),
		"responseNonce=2 should Omit without force_height_sync_anchor (K=10, slots=1)")

	txs3 := pipelineInferenceTxs(env.server.Host().MempoolTxs(), testutil.StartTx(3))
	diff3 := testutil.SignDiff(t, env.userSigner, "escrow-1", 3, txs3)
	dj3, err := DiffToJSON(diff3)
	require.NoError(t, err)
	ir3 := InferenceRequest{
		Diffs:                 []DiffJSON{dj3},
		Nonce:                 3,
		ForceHeightSyncAnchor: true,
		Payload:               payload,
	}
	body3, err := json.Marshal(ir3)
	require.NoError(t, err)
	rec3 := env.doPost(t, "/devshard/v2/sessions/escrow-1/chat/completions", body3)
	require.Equal(t, http.StatusOK, rec3.Code, rec3.Body.String())
	require.True(t, sseFirstReceiptHasHeightSync(rec3.Body.String()),
		"force_height_sync_anchor must emit Anchor on responseNonce=3")

	ring := env.server.HeightSyncAuditRing()
	require.NotNil(t, ring)
	localAddr := env.hostSigner.Address()
	nResp := 0
	for _, a := range ring.List(localAddr) {
		if a.Direction == "response" && a.MainnetHeight == 77 {
			nResp++
		}
	}
	require.GreaterOrEqual(t, nResp, 2, "expect anchors at least for cadence response 1 and forced response 3")
}

type discardRestLogger struct{}

func (discardRestLogger) Info(string, ...any)  {}
func (discardRestLogger) Error(string, ...any) {}
func (discardRestLogger) Warn(string, ...any)  {}
func (discardRestLogger) Debug(string, ...any) {}

type warnCaptureLogger struct {
	warns []string
	discardRestLogger
}

func (w *warnCaptureLogger) Warn(msg string, kv ...any) {
	w.warns = append(w.warns, formatLogLine(msg, kv))
}

func formatLogLine(msg string, kv []any) string {
	if len(kv) == 0 {
		return msg
	}
	var b strings.Builder
	b.WriteString(msg)
	for i := 0; i+1 < len(kv); i += 2 {
		fmt.Fprintf(&b, " %v=%v", kv[i], kv[i+1])
	}
	return b.String()
}

type mutableTestOracle struct {
	mu  sync.Mutex
	hdr *blocks.Header
}

func (o *mutableTestOracle) Latest(context.Context) (*blocks.Header, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.hdr == nil {
		return nil, nil
	}
	h := *o.hdr
	h.BlockHash = append([]byte(nil), o.hdr.BlockHash...)
	return &h, nil
}

func (o *mutableTestOracle) At(context.Context, int64) (*blocks.Header, error) { return nil, nil }

func (o *mutableTestOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, nil
}

func (o *mutableTestOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func TestServer_Inference_HeightSync_UntrustedReconcileMismatchWarns(t *testing.T) {
	capLog := &warnCaptureLogger{}
	logging.SetLogger(capLog)
	t.Cleanup(func() { logging.SetLogger(discardRestLogger{}) })

	or := &mutableTestOracle{hdr: &blocks.Header{
		Height:    10,
		ChainID:   "chain-x",
		BlockHash: bytes.Repeat([]byte{0x01}, 32),
	}}
	sched := heightsync.MustNewAnchorSchedulerFromOracle(10, 1, or)
	env := setupServerEnv(t, WithHeightSync(sched, or))

	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	dj, err := DiffToJSON(diff)
	require.NoError(t, err)
	ir := InferenceRequest{
		Diffs:   []DiffJSON{dj},
		Nonce:   1,
		Payload: &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000},
	}
	peerHash := hex.EncodeToString(bytes.Repeat([]byte{0xbb}, 32))
	hs := &heightsync.HeightSyncSection{
		ChainID:             "chain-x",
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       11,
		MainnetBlockHashHex: peerHash,
		TimestampUnixMs:     time.Now().UnixMilli(),
		Direction:           "request",
	}
	wrapBody, err := MarshalWrappedInferenceRequest(CurrentInferenceEnvelopeSchemaVersion, hs, ir)
	require.NoError(t, err)

	rec := env.doPostContentType(t, "/devshard/v2/sessions/escrow-1/chat/completions", "application/x-protobuf", wrapBody)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Empty(t, capLog.warns)

	ring := env.server.HeightSyncAuditRing()
	userAddr := env.userSigner.Address()
	var sawUntrusted bool
	for _, a := range ring.List(userAddr) {
		if a.Direction == "request" && a.MainnetHeight == 11 {
			require.Equal(t, heightsync.TrustUntrustedPeer, a.Trust)
			sawUntrusted = true
			break
		}
	}
	require.True(t, sawUntrusted)

	or.mu.Lock()
	or.hdr.Height = 11
	or.hdr.BlockHash = bytes.Repeat([]byte{0xcc}, 32)
	or.mu.Unlock()

	diff2 := testutil.SignDiff(t, env.userSigner, "escrow-1", 2, nil)
	dj2, err := DiffToJSON(diff2)
	require.NoError(t, err)
	ir2 := InferenceRequest{
		Diffs:   []DiffJSON{dj2},
		Nonce:   2,
		Payload: ir.Payload,
		Stream:  ir.Stream,
	}
	body2, err := json.Marshal(ir2)
	require.NoError(t, err)
	rec2 := env.doPost(t, "/devshard/v2/sessions/escrow-1/chat/completions", body2)
	require.Equal(t, http.StatusOK, rec2.Code)

	require.NotEmpty(t, capLog.warns)
	require.Contains(t, capLog.warns[0], "untrusted peer tip disagrees")
}

func TestServer_Inference_HeightSync_UntrustedReconcileMatchNoWarn(t *testing.T) {
	capLog := &warnCaptureLogger{}
	logging.SetLogger(capLog)
	t.Cleanup(func() { logging.SetLogger(discardRestLogger{}) })

	matchHash := bytes.Repeat([]byte{0xbb}, 32)
	or := &mutableTestOracle{hdr: &blocks.Header{
		Height:    10,
		ChainID:   "chain-x",
		BlockHash: bytes.Repeat([]byte{0x01}, 32),
	}}
	sched := heightsync.MustNewAnchorSchedulerFromOracle(10, 1, or)
	env := setupServerEnv(t, WithHeightSync(sched, or))

	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	dj, err := DiffToJSON(diff)
	require.NoError(t, err)
	ir := InferenceRequest{
		Diffs:   []DiffJSON{dj},
		Nonce:   1,
		Payload: &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000},
	}
	peerHash := hex.EncodeToString(matchHash)
	hs := &heightsync.HeightSyncSection{
		ChainID:             "chain-x",
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       11,
		MainnetBlockHashHex: peerHash,
		Direction:           "request",
	}
	wrapBody, err := MarshalWrappedInferenceRequest(CurrentInferenceEnvelopeSchemaVersion, hs, ir)
	require.NoError(t, err)
	rec := env.doPostContentType(t, "/devshard/v2/sessions/escrow-1/chat/completions", "application/x-protobuf", wrapBody)
	require.Equal(t, http.StatusOK, rec.Code)

	or.mu.Lock()
	or.hdr.Height = 11
	or.hdr.BlockHash = append([]byte(nil), matchHash...)
	or.mu.Unlock()

	diff2 := testutil.SignDiff(t, env.userSigner, "escrow-1", 2, nil)
	dj2, err := DiffToJSON(diff2)
	require.NoError(t, err)
	ir2 := InferenceRequest{
		Diffs:   []DiffJSON{dj2},
		Nonce:   2,
		Payload: ir.Payload,
		Stream:  ir.Stream,
	}
	body2, err := json.Marshal(ir2)
	require.NoError(t, err)
	rec2 := env.doPost(t, "/devshard/v2/sessions/escrow-1/chat/completions", body2)
	require.Equal(t, http.StatusOK, rec2.Code)
	require.Empty(t, capLog.warns)
}

// TestServer_Inference_HeightSync_ForcedTurn_HostAnchorsEvenIfRequestOmits
// covers the malicious-user variant of the forced sync turn (spec §9):
//
//   - The trigger diff carries MsgForceHeightSyncTurn at nonce 2; the host
//     applies it via catch-up and its escrow state opens a forced window.
//   - The user's HTTP envelope at nonce 2 is plain JSON (no height_sync).
//   - The server MUST still process the request and emit Anchor on the SSE
//     receipt (response side is normatively bound by the host's own state).
//   - The server MUST log a "heightsync: force_request_anchor_missing" warn
//     entry and append a TrustForceRequestAnchorMissing audit-ring entry
//     against the inbound peer id (request side is best-effort + dispute
//     evidence, not a rejection).
func TestServer_Inference_HeightSync_ForcedTurn_HostAnchorsEvenIfRequestOmits(t *testing.T) {
	capLog := &warnCaptureLogger{}
	logging.SetLogger(capLog)
	t.Cleanup(func() { logging.SetLogger(discardRestLogger{}) })

	or := &heightSyncTestOracle{hdr: &blocks.Header{
		Height:    77,
		ChainID:   "chain-x",
		BlockHash: bytes.Repeat([]byte{0xa1}, 32),
	}}
	// K=10, slots=1: cadence anchors at nonces 1 and 10. Nonce 2 would
	// normally Omit, so any Anchor at nonce 2 is attributable to the
	// forced turn alone.
	sched := heightsync.MustNewAnchorSchedulerFromOracle(10, 1, or)
	env := setupServerEnv(t, WithHeightSync(sched, or))

	payload := &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}

	diff1 := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	dj1, err := DiffToJSON(diff1)
	require.NoError(t, err)
	ir1 := InferenceRequest{Diffs: []DiffJSON{dj1}, Nonce: 1, Payload: payload}
	body1, err := json.Marshal(ir1)
	require.NoError(t, err)
	rec1 := env.doPost(t, "/devshard/v2/sessions/escrow-1/chat/completions", body1)
	require.Equal(t, http.StatusOK, rec1.Code, rec1.Body.String())

	require.False(t, env.server.host.HeightSyncEscrowHints(10, 1) != nil &&
		env.server.host.HeightSyncEscrowHints(10, 1).ForcedEnd != 0,
		"sanity: no forced turn before trigger diff is applied")

	// Trigger diff at nonce 2: MsgForceHeightSyncTurn opens window [2,2]
	// (single-host group → slots=1). The matching StartInference covers the
	// inference path so the request still produces a normal receipt.
	forceTx := &types.DevshardTx{Tx: &types.DevshardTx_ForceHeightSyncTurn{
		ForceHeightSyncTurn: &types.MsgForceHeightSyncTurn{
			TriggerNonce: 2,
			EndNonce:     2,
			AnchorK:      10,
			SlotsNum:     1,
			Reason:       "test_malicious_user",
		},
	}}
	startTx2 := testutil.StartTx(2)
	diff2 := testutil.SignDiff(t, env.userSigner, "escrow-1", 2, []*types.DevshardTx{forceTx, startTx2})
	dj2, err := DiffToJSON(diff2)
	require.NoError(t, err)

	// Plain-JSON envelope — no height_sync section. Simulates a malicious
	// user that signed the trigger diff but strips the Anchor on the wire.
	ir2 := InferenceRequest{Diffs: []DiffJSON{dj2}, Nonce: 2, Payload: payload}
	body2, err := json.Marshal(ir2)
	require.NoError(t, err)

	rec2 := env.doPost(t, "/devshard/v2/sessions/escrow-1/chat/completions", body2)
	require.Equal(t, http.StatusOK, rec2.Code, rec2.Body.String())

	require.True(t, sseFirstReceiptHasHeightSync(rec2.Body.String()),
		"host response at nonce=2 inside forced window MUST Anchor regardless of user's missing inbound Anchor")

	postH := env.server.host.HeightSyncEscrowHints(10, 1)
	require.NotNil(t, postH, "forced turn must be live in escrow state after diff applies")
	require.Equal(t, uint64(2), postH.ForcedStart)
	require.Equal(t, uint64(2), postH.ForcedEnd)

	require.True(t, warnsContain(capLog.warns, "heightsync: force_request_anchor_missing"),
		"server must log a warn entry when an in-window request omits height_sync")

	ring := env.server.HeightSyncAuditRing()
	require.NotNil(t, ring)
	userAddr := env.userSigner.Address()
	var sawMissing bool
	for _, a := range ring.List(userAddr) {
		if a.Trust == heightsync.TrustForceRequestAnchorMissing && a.Direction == "request" {
			require.Zero(t, a.MainnetHeight, "missing-anchor sentinel must carry no oracle data")
			require.Empty(t, a.MainnetBlockHash, "missing-anchor sentinel must carry no oracle data")
			sawMissing = true
			break
		}
	}
	require.True(t, sawMissing,
		"audit ring must contain a TrustForceRequestAnchorMissing entry for the in-window request")
}

func courierCarryForwardHS(height int64, originator string, observedAt time.Time) *heightsync.HeightSyncSection {
	return &heightsync.HeightSyncSection{
		ChainID:               "chain-x",
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         height,
		MainnetBlockHashHex:   hex.EncodeToString(bytes.Repeat([]byte{0xab}, 32)),
		TimestampUnixMs:       observedAt.UnixMilli(),
		OriginatorSenderID:    originator,
		OriginatorTimestampMs: observedAt.UnixMilli(),
		Direction:             "request",
	}
}

func advanceSessionToNonce(t *testing.T, env *serverTestEnv, targetNonce uint64) {
	t.Helper()
	for n := uint64(1); n < targetNonce; n++ {
		rec := postProtobufInference(t, env, n, nil)
		require.Equal(t, http.StatusOK, rec.Code, "advance nonce=%d: %s", n, rec.Body.String())
	}
}

func postProtobufInference(t *testing.T, env *serverTestEnv, nonce uint64, hs *heightsync.HeightSyncSection) *httptest.ResponseRecorder {
	t.Helper()
	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", nonce, []*types.DevshardTx{testutil.StartTx(nonce)})
	dj, err := DiffToJSON(diff)
	require.NoError(t, err)
	ir := InferenceRequest{
		Diffs:   []DiffJSON{dj},
		Nonce:   nonce,
		Payload: &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000},
	}
	var body []byte
	if hs != nil {
		body, err = MarshalWrappedInferenceRequest(CurrentInferenceEnvelopeSchemaVersion, hs, ir)
		require.NoError(t, err)
		return env.doPostContentType(t, "/devshard/v2/sessions/escrow-1/chat/completions", "application/x-protobuf", body)
	}
	body, err = json.Marshal(ir)
	require.NoError(t, err)
	return env.doPost(t, "/devshard/v2/sessions/escrow-1/chat/completions", body)
}

// TestServer_LazyAnchorAcceptedOutsideSyncTurn covers spec §16: VALID_LAZY_ANCHOR on omit-window nonces.
func TestServer_LazyAnchorAcceptedOutsideSyncTurn(t *testing.T) {
	or := &mutableTestOracle{hdr: &blocks.Header{
		Height:    10,
		ChainID:   "chain-x",
		BlockHash: bytes.Repeat([]byte{0x01}, 32),
	}}
	sched := heightsync.MustNewAnchorSchedulerFromOracle(8, 4, or)
	env := setupServerEnv(t, WithHeightSync(sched, or))

	advanceSessionToNonce(t, env, 5)
	now := time.Now()
	hs := courierCarryForwardHS(11, "gonka1origin", now)
	rec := postProtobufInference(t, env, 5, hs)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	ring := env.server.HeightSyncAuditRing()
	userAddr := env.userSigner.Address()
	var sawLazy bool
	for _, a := range ring.List(userAddr) {
		if a.Direction == "request" && a.MainnetHeight == 11 && a.Trust == heightsync.TrustUntrustedPeer && a.Tag == heightsync.TagLazy {
			sawLazy = true
			break
		}
	}
	require.True(t, sawLazy, "omit-window carry-forward must be audit-tagged lazy")
}

// TestServer_StaleOriginRejected covers the spec §16 freshness gate (reason=stale_origin).
func TestServer_StaleOriginRejected(t *testing.T) {
	capLog := &warnCaptureLogger{}
	logging.SetLogger(capLog)
	t.Cleanup(func() { logging.SetLogger(discardRestLogger{}) })

	or := &mutableTestOracle{hdr: &blocks.Header{
		Height:    10,
		ChainID:   "chain-x",
		BlockHash: bytes.Repeat([]byte{0x01}, 32),
	}}
	sched := heightsync.MustNewAnchorSchedulerFromOracle(8, 4, or)
	env := setupServerEnv(t, WithHeightSync(sched, or))

	advanceSessionToNonce(t, env, 5)
	staleAt := time.Now().Add(-5 * time.Minute)
	hs := courierCarryForwardHS(11, "gonka1origin", staleAt)
	rec := postProtobufInference(t, env, 5, hs)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var sawStaleWarn bool
	for _, w := range capLog.warns {
		if strings.Contains(w, "stale_origin") {
			sawStaleWarn = true
			break
		}
	}
	require.True(t, sawStaleWarn, "stale carry-forward must log warn with stale_origin")

	ring := env.server.HeightSyncAuditRing()
	userAddr := env.userSigner.Address()
	var sawDispute bool
	for _, a := range ring.List(userAddr) {
		if a.Trust == heightsync.TrustDisputeCarrier {
			sawDispute = true
			require.NotEqual(t, heightsync.TagLazy, a.Tag)
		}
		if a.Tag == heightsync.TagLazy {
			t.Fatal("stale origin must not produce a lazy-tagged accepted anchor")
		}
	}
	require.True(t, sawDispute, "audit ring must record dispute_carrier for stale origin")
}

// TestServer_LazyAnchorInsideSyncTurn_IsCadenceAnchor covers spec §9 / §16: carry-forward inside sync turn is cadence, not lazy.
func TestServer_LazyAnchorInsideSyncTurn_IsCadenceAnchor(t *testing.T) {
	or := &mutableTestOracle{hdr: &blocks.Header{
		Height:    10,
		ChainID:   "chain-x",
		BlockHash: bytes.Repeat([]byte{0x01}, 32),
	}}
	sched := heightsync.MustNewAnchorSchedulerFromOracle(8, 4, or)
	env := setupServerEnv(t, WithHeightSync(sched, or))

	advanceSessionToNonce(t, env, 2)
	hs := courierCarryForwardHS(11, "gonka1origin", time.Now())
	rec := postProtobufInference(t, env, 2, hs)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	ring := env.server.HeightSyncAuditRing()
	userAddr := env.userSigner.Address()
	var sawCadence bool
	for _, a := range ring.List(userAddr) {
		if a.Direction == "request" && a.MainnetHeight == 11 && a.Tag == heightsync.TagCadence {
			sawCadence = true
			break
		}
	}
	require.True(t, sawCadence, "sync-turn carry-forward must be audit-tagged cadence")
}

func TestHandleHeightSync_DisabledReturnsNotFound(t *testing.T) {
	or := &heightSyncTestOracle{hdr: &blocks.Header{
		Height: 10, ChainID: "c", BlockHash: []byte{0x01},
	}}
	sched := heightsync.MustNewAnchorSchedulerFromOracle(8, 4, or)
	env := setupServerEnv(t, WithHeightSync(sched, or), WithHeightSyncSeedRPC(false))

	rec := env.doPost(t, "/devshard/v2/sessions/escrow-1/height-sync", []byte("{}"))
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestHandleHeightSync_DefaultOnWhenHeightSyncEnabled(t *testing.T) {
	or := &heightSyncTestOracle{hdr: &blocks.Header{
		Height: 10, ChainID: "c", BlockHash: []byte{0x01},
	}}
	sched := heightsync.MustNewAnchorSchedulerFromOracle(8, 4, or)
	env := setupServerEnv(t, WithHeightSync(sched, or))

	rec := env.doPost(t, "/devshard/v2/sessions/escrow-1/height-sync", []byte("{}"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestHandleHeightSync_ForcesAnchor(t *testing.T) {
	or := &heightSyncTestOracle{hdr: &blocks.Header{
		Height:    88,
		ChainID:   "chain-seed",
		BlockHash: []byte{0xca, 0xfe},
	}}
	sched := heightsync.MustNewAnchorSchedulerFromOracle(8, 4, or)
	env := setupServerEnv(t, WithHeightSync(sched, or), WithHeightSyncSeedRPC(true))

	rec := env.doPost(t, "/devshard/v2/sessions/escrow-1/height-sync", []byte("{}"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var out heightSyncSeedResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotNil(t, out.HeightSync)
	require.Equal(t, heightsync.AnchorProofType, out.HeightSync.ProofType)
	require.Equal(t, int64(88), out.HeightSync.MainnetHeight)
	require.Equal(t, "response", out.HeightSync.Direction)
	require.Equal(t, env.hostSigner.Address(), out.HeightSync.OriginatorSenderID)

	ring := env.server.HeightSyncAuditRing()
	require.NotNil(t, ring)
	localAddr := env.hostSigner.Address()
	var saw bool
	for _, a := range ring.List(localAddr) {
		if a.Direction == "response" && a.MainnetHeight == 88 {
			saw = true
			break
		}
	}
	require.True(t, saw, "seed RPC must record outbound anchor in audit ring")
}

func TestServer_ResponseAnchor_SignedByHost(t *testing.T) {
	or := &heightSyncTestOracle{hdr: &blocks.Header{
		Height: 33, ChainID: "c", BlockHash: []byte{0x01},
	}}
	sched := heightsync.MustNewAnchorSchedulerFromOracle(8, 4, or)
	env := setupServerEnv(t, WithHeightSync(sched, or), WithHeightSyncSeedRPC(true))

	rec := env.doPost(t, "/devshard/v2/sessions/escrow-1/height-sync", []byte("{}"))
	require.Equal(t, http.StatusOK, rec.Code)

	var out heightSyncSeedResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotNil(t, out.HeightSync)
	require.NotEmpty(t, out.HeightSync.SenderSignature)
	require.Equal(t, env.hostSigner.Address(), out.HeightSync.OriginatorSenderID)
	require.NoError(t, heightsync.VerifyOrigin(signing.NewSecp256k1Verifier(), out.HeightSync, out.HeightSync.SenderSignature))
}

func TestServer_RequestLeg_DoesNotVerifyOriginSig(t *testing.T) {
	or := &heightSyncTestOracle{hdr: &blocks.Header{
		Height: 44, ChainID: "c", BlockHash: []byte{0x02},
	}}
	sched := heightsync.MustNewAnchorSchedulerFromOracle(8, 4, or)
	env := setupServerEnv(t, WithHeightSync(sched, or))

	// Unsigned carry-forward from user is accepted on sync-turn nonce 1.
	hs := courierCarryForwardHS(44, "gonka1external", time.Now())
	require.Empty(t, hs.SenderSignature)
	rec := postProtobufInference(t, env, 1, hs)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func postHeartbeatProtobuf(t *testing.T, env *serverTestEnv, nonce uint64, height uint64, hash []byte, hs *heightsync.HeightSyncSection) *httptest.ResponseRecorder {
	t.Helper()
	hb := &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		TurnSeq:           1,
		ObservedHeight:    height,
		ObservedBlockHash: hash,
		SlotsNum:          1,
		Reason:            string(heightsync.ReasonQuietSession),
	}}}
	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", nonce, []*types.DevshardTx{hb})
	dj, err := DiffToJSON(diff)
	require.NoError(t, err)
	ir := InferenceRequest{
		Diffs:   []DiffJSON{dj},
		Nonce:   nonce,
		Payload: &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000},
	}
	body, err := MarshalWrappedInferenceRequest(CurrentInferenceEnvelopeSchemaVersion, hs, ir)
	require.NoError(t, err)
	return env.doPostContentType(t, "/devshard/v2/sessions/escrow-1/chat/completions", "application/x-protobuf", body)
}

func TestHeightAck_EnvelopeBindingMismatch(t *testing.T) {
	or := &heightSyncTestOracle{hdr: &blocks.Header{
		Height:    77,
		ChainID:   "chain-x",
		BlockHash: []byte{0xab, 0xcd},
	}}
	sched := heightsync.MustNewAnchorSchedulerFromOracle(10, 1, or)
	env := setupServerEnvHost(t, []host.HostOption{host.WithChainOracle(or)}, WithHeightSync(sched, or))
	env.server.SetHeightSyncResponseAfterSignHook(func(sec *heightsync.HeightSyncSection, nonce uint64) {
		sec.MainnetHeight = 99
	})

	rec := postHeartbeatProtobuf(t, env, 1, 77, []byte{0xab, 0xcd}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, env.server.HeightSyncMarks().HasKind(heightsync.MarkDisputeOriginator))
}

// TestHeartbeat_RequestLegBindingMismatch is the understatement half of L4's
// request leg: the sequencer signs an envelope saying it sees 900 and writes 77
// into the log under the same identity. The lift direction is legal and is
// covered by TestLogPlane_HeartbeatLiftDoesNotTripEnvelopeBinding.
func TestHeartbeat_RequestLegBindingMismatch(t *testing.T) {
	or := &heightSyncTestOracle{hdr: &blocks.Header{
		Height:    77,
		ChainID:   "chain-x",
		BlockHash: []byte{0xab, 0xcd},
	}}
	sched := heightsync.MustNewAnchorSchedulerFromOracle(10, 1, or)
	env := setupServerEnvHost(t, []host.HostOption{host.WithChainOracle(or)}, WithHeightSync(sched, or))

	hs := &heightsync.HeightSyncSection{
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       900,
		MainnetBlockHashHex: "abcd",
		Direction:           "request",
	}
	rec := postHeartbeatProtobuf(t, env, 1, 77, []byte{0xab, 0xcd}, hs)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String(), "L4 must not reject the HTTP exchange")
	require.True(t, env.server.HeightSyncMarks().HasKind(heightsync.MarkDisputeCarrier))
	for _, m := range env.server.HeightSyncMarks().All() {
		require.LessOrEqual(t, len(m.Blob), heightsync.MaxMarkBlobBytes)
	}
}

func TestLogPlane_SectionPresentForOneRecipientOnly(t *testing.T) {
	or := &heightSyncTestOracle{hdr: &blocks.Header{
		Height:    77,
		ChainID:   "chain-x",
		BlockHash: []byte{0xab, 0xcd},
	}}
	schedA := heightsync.MustNewAnchorSchedulerFromOracle(10, 1, or)
	schedB := heightsync.MustNewAnchorSchedulerFromOracle(10, 1, or)
	envA := setupServerEnvHost(t, []host.HostOption{host.WithChainOracle(or)}, WithHeightSync(schedA, or))
	envB := setupServerEnvHost(t, []host.HostOption{host.WithChainOracle(or)}, WithHeightSync(schedB, or))

	hs := &heightsync.HeightSyncSection{
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       900,
		MainnetBlockHashHex: "abcd",
		Direction:           "request",
	}
	recA := postHeartbeatProtobuf(t, envA, 1, 77, []byte{0xab, 0xcd}, hs)
	recB := postHeartbeatProtobuf(t, envB, 1, 77, []byte{0xab, 0xcd}, nil)
	require.Equal(t, http.StatusOK, recA.Code, recA.Body.String())
	require.Equal(t, http.StatusOK, recB.Code, recB.Body.String())
	require.True(t, envA.server.HeightSyncMarks().HasKind(heightsync.MarkDisputeCarrier))
	require.False(t, envB.server.HeightSyncMarks().HasKind(heightsync.MarkDisputeCarrier))
}

type failOriginSigner struct{}

func (failOriginSigner) Address() string { return "fail" }

func (failOriginSigner) Sign([]byte) ([]byte, error) {
	return nil, errors.New("injected origin sign failure")
}

func TestHandleHeightSync_OmitsSectionOnSignFailure(t *testing.T) {
	or := &heightSyncTestOracle{hdr: &blocks.Header{
		Height:    88,
		ChainID:   "chain-seed",
		BlockHash: []byte{0xca, 0xfe},
	}}
	sched := heightsync.MustNewAnchorSchedulerFromOracle(8, 4, or)
	env := setupServerEnv(t, WithHeightSync(sched, or), WithHeightSyncSeedRPC(true))
	env.server.SetHeightSyncOriginSigner(failOriginSigner{})

	rec := env.doPost(t, "/devshard/v2/sessions/escrow-1/height-sync", []byte("{}"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var out heightSyncSeedResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Nil(t, out.HeightSync, "unsigned Anchor must be omitted, not sent")
}
