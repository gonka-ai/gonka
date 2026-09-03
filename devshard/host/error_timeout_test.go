package host

import (
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"common/completionapi"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
)

// Compile-time guard: VerifyErrorMiss must not grow a ctx, ExecutorClient,
// payload fetcher, SessionConfig, or clock. Adding any of those is a protocol
// regression (verifiers would wait or contact the executor).
var _ func(
	types.EscrowState,
	uint64,
	[]byte,
	[]byte,
	[]*types.DevshardTx,
	FinishProposerVerifier,
) (bool, []byte, string, error) = VerifyErrorMiss

var engineCoreEvents = []string{
	`data: {"error":{"code":500,"message":"EngineCore encountered an issue. See stack trace (above) for the root cause.","param":null,"type":"InternalServerError"},"id":"devshard-57577-89"}`,
	`data: [DONE]`,
}

var contentThenErrorEvents = []string{
	`data: {"id":"devshard-1-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello"}}]}`,
	`data: {"error":{"code":500,"message":"boom","type":"InternalServerError"},"id":"devshard-1-1"}`,
	`data: [DONE]`,
}

type errorTimeoutEnv struct {
	sm       *state.StateMachine
	hosts    []*signing.Secp256k1Signer
	st       types.EscrowState
	payload  []byte
	hash     []byte
	finish   *types.MsgFinishInference
	finishTx []byte
}

func streamedPayload(t *testing.T, events []string) []byte {
	t.Helper()
	b, err := json.Marshal(completionapi.SerializedStreamedResponse{Events: events})
	require.NoError(t, err)
	return b
}

func payloadSHA256(payload []byte) []byte {
	sum := sha256.Sum256(payload)
	return sum[:]
}

func marshalFinishTx(t *testing.T, msg *types.MsgFinishInference) []byte {
	t.Helper()
	tx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}
	b, err := proto.Marshal(tx)
	require.NoError(t, err)
	return b
}

func signedErrorFinish(t *testing.T, hosts []*signing.Secp256k1Signer, inferenceID uint64, executorSlot uint32, outputTokens uint64, responseHash []byte) *types.MsgFinishInference {
	t.Helper()
	msg := &types.MsgFinishInference{
		InferenceId:  inferenceID,
		ResponseHash: responseHash,
		InputTokens:  0,
		OutputTokens: outputTokens,
		ExecutorSlot: executorSlot,
		EscrowId:     "escrow-1",
	}
	msg.ProposerSig = testutil.SignProposerTx(t, hosts[executorSlot], msg)
	return msg
}

func newErrorTimeoutSM(t *testing.T) (*state.StateMachine, []*signing.Secp256k1Signer) {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine(
		"escrow-1", config, group, 10000, user.Address(), verifier,
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000),
	)
	require.NoError(t, err)
	return sm, hosts
}

func newErrorTimeoutEnv(t *testing.T) *errorTimeoutEnv {
	t.Helper()
	sm, hosts := newErrorTimeoutSM(t)
	payload := streamedPayload(t, engineCoreEvents)
	hash := payloadSHA256(payload)
	finish := signedErrorFinish(t, hosts, 1, 1, 0, hash)
	st := stateWithStarted(1, 1)
	return &errorTimeoutEnv{
		sm:       sm,
		hosts:    hosts,
		st:       st,
		payload:  payload,
		hash:     hash,
		finish:   finish,
		finishTx: marshalFinishTx(t, finish),
	}
}

func (e *errorTimeoutEnv) verify(finishTx, payload []byte, mempool []*types.DevshardTx) (bool, error) {
	accept, _, _, err := VerifyErrorMiss(e.st, 1, finishTx, payload, mempool, e.sm)
	return accept, err
}

func TestVerifyErrorMiss_NoWaitGuard(t *testing.T) {
	e := newErrorTimeoutEnv(t)
	now := time.Now().Unix()
	e.st = stateWithStartedAt(1, 1, now)
	require.Greater(t, e.st.Config.RefusalTimeout, int64(0))
	require.Greater(t, e.st.Config.ExecutionTimeout, int64(0))
	require.Equal(t, now, e.st.Inferences[1].ConfirmedAt)

	accept, err := e.verify(e.finishTx, e.payload, nil)
	require.NoError(t, err)
	require.True(t, accept, "ERROR timeout must not wait on RefusalTimeout or ExecutionTimeout")
}

func TestVerifyErrorMiss_NoNetworkGuard(t *testing.T) {
	e := newErrorTimeoutEnv(t)
	fetcher := func() {
		t.Fatal("payload fetcher must not be called")
	}
	_ = fetcher
	var executor ExecutorClient // nil: contacting it would panic
	_ = executor

	accept, hash, _, err := VerifyErrorMiss(e.st, 1, e.finishTx, e.payload, nil, e.sm)
	require.NoError(t, err)
	require.True(t, accept, "ERROR timeout must accept from finish_tx + payload with empty mempool and no executor client")
	require.Equal(t, e.hash, hash)
}

func TestVerifyErrorMiss_ValidStartedAccepts(t *testing.T) {
	e := newErrorTimeoutEnv(t)
	require.Equal(t, types.StatusStarted, e.st.Inferences[1].Status)

	accept, err := e.verify(e.finishTx, e.payload, nil)
	require.NoError(t, err)
	require.True(t, accept)
	require.Equal(t, e.hash, e.finish.ResponseHash, "vote in step 6 is signed over this hash")
}

func TestVerifyErrorMiss_LyingHostOutputTokensIgnored(t *testing.T) {
	e := newErrorTimeoutEnv(t)
	e.finish = signedErrorFinish(t, e.hosts, 1, 1, 7, e.hash)
	e.finishTx = marshalFinishTx(t, e.finish)

	accept, err := e.verify(e.finishTx, e.payload, nil)
	require.NoError(t, err)
	require.True(t, accept, "body decides; OutputTokens=7 on an error envelope still accepts")
}

func TestVerifyErrorMiss_ContentThenErrorAccepts(t *testing.T) {
	e := newErrorTimeoutEnv(t)
	payload := streamedPayload(t, contentThenErrorEvents)
	hash := payloadSHA256(payload)
	finish := signedErrorFinish(t, e.hosts, 1, 1, 0, hash)

	accept, err := e.verify(marshalFinishTx(t, finish), payload, nil)
	require.NoError(t, err)
	require.True(t, accept, "an error envelope on a signed Finish is a miss even after content")
}

func TestVerifyErrorMiss_HashMismatchRejects(t *testing.T) {
	e := newErrorTimeoutEnv(t)
	other := streamedPayload(t, []string{
		`data: {"error":{"code":500,"message":"different body","type":"InternalServerError"},"id":"devshard-1-1"}`,
		`data: [DONE]`,
	})
	require.NotEqual(t, e.hash, payloadSHA256(other))

	accept, err := e.verify(e.finishTx, other, nil)
	require.NoError(t, err)
	require.False(t, accept)
}

func TestVerifyErrorMiss_EmptyPayloadRejects(t *testing.T) {
	e := newErrorTimeoutEnv(t)
	for _, payload := range [][]byte{nil, {}} {
		accept, err := e.verify(e.finishTx, payload, nil)
		require.NoError(t, err)
		require.False(t, accept)
	}
}

func TestVerifyErrorMiss_TamperedFinishRejects(t *testing.T) {
	e := newErrorTimeoutEnv(t)

	t.Run("non-executor signer", func(t *testing.T) {
		msg := &types.MsgFinishInference{
			InferenceId:  1,
			ResponseHash: e.hash,
			ExecutorSlot: 1,
			EscrowId:     "escrow-1",
		}
		msg.ProposerSig = testutil.SignProposerTx(t, e.hosts[0], msg)
		accept, err := e.verify(marshalFinishTx(t, msg), e.payload, nil)
		require.NoError(t, err)
		require.False(t, accept)
	})

	t.Run("fields edited after signing", func(t *testing.T) {
		msg := proto.Clone(e.finish).(*types.MsgFinishInference)
		msg.OutputTokens = 99
		accept, err := e.verify(marshalFinishTx(t, msg), e.payload, nil)
		require.NoError(t, err)
		require.False(t, accept)
	})
}

func TestVerifyErrorMiss_AbsentOrUndecodableFinishRejects(t *testing.T) {
	e := newErrorTimeoutEnv(t)

	t.Run("absent", func(t *testing.T) {
		accept, err := e.verify(nil, e.payload, nil)
		require.NoError(t, err)
		require.False(t, accept)
	})

	t.Run("undecodable", func(t *testing.T) {
		accept, err := e.verify([]byte("not a proto"), e.payload, nil)
		require.NoError(t, err)
		require.False(t, accept)
	})

	t.Run("wrong tx type", func(t *testing.T) {
		tx := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1}}}
		b, err := proto.Marshal(tx)
		require.NoError(t, err)
		accept, vErr := e.verify(b, e.payload, nil)
		require.NoError(t, vErr)
		require.False(t, accept)
	})
}

func TestVerifyErrorMiss_WrongInferenceOrEscrowRejects(t *testing.T) {
	e := newErrorTimeoutEnv(t)

	t.Run("different inference", func(t *testing.T) {
		finish := signedErrorFinish(t, e.hosts, 2, 1, 0, e.hash)
		accept, err := e.verify(marshalFinishTx(t, finish), e.payload, nil)
		require.NoError(t, err)
		require.False(t, accept)
	})

	t.Run("different escrow", func(t *testing.T) {
		msg := &types.MsgFinishInference{
			InferenceId:  1,
			ResponseHash: e.hash,
			ExecutorSlot: 1,
			EscrowId:     "escrow-other",
		}
		msg.ProposerSig = testutil.SignProposerTx(t, e.hosts[1], msg)
		accept, err := e.verify(marshalFinishTx(t, msg), e.payload, nil)
		require.NoError(t, err)
		require.False(t, accept)
	})
}

func TestVerifyErrorMiss_FinishedRecordHashMismatchRejects(t *testing.T) {
	e := newErrorTimeoutEnv(t)
	e.st.Inferences[1].Status = types.StatusFinished
	e.st.Inferences[1].ResponseHash = []byte("applied-hash-that-does-not-match")

	accept, err := e.verify(e.finishTx, e.payload, nil)
	require.NoError(t, err)
	require.False(t, accept)
}

func TestVerifyErrorMiss_RoundTripEngineCoreHash(t *testing.T) {
	e := newErrorTimeoutEnv(t)
	rebuilt := streamedPayload(t, engineCoreEvents)
	require.Equal(t, e.hash, payloadSHA256(rebuilt), "gateway-rebuilt EngineCore payload must hash to the executor ResponseHash")

	accept, err := e.verify(e.finishTx, rebuilt, nil)
	require.NoError(t, err)
	require.True(t, accept)
}

func TestVerifyErrorMiss_FinishedRecordMatchingHashAccepts(t *testing.T) {
	e := newErrorTimeoutEnv(t)
	e.st.Inferences[1].Status = types.StatusFinished
	e.st.Inferences[1].ResponseHash = append([]byte(nil), e.hash...)

	accept, err := e.verify(e.finishTx, e.payload, nil)
	require.NoError(t, err)
	require.True(t, accept)
}

func TestVerifyErrorMiss_MempoolFallbackWhenFinishTxAbsent(t *testing.T) {
	e := newErrorTimeoutEnv(t)
	mempool := []*types.DevshardTx{
		{Tx: &types.DevshardTx_FinishInference{FinishInference: e.finish}},
	}
	accept, err := e.verify(nil, e.payload, mempool)
	require.NoError(t, err)
	require.True(t, accept)
}

func TestVerifyErrorMiss_ReturnsFinishHash(t *testing.T) {
	e := newErrorTimeoutEnv(t)
	accept, hash, _, err := VerifyErrorMiss(e.st, 1, e.finishTx, e.payload, nil, e.sm)
	require.NoError(t, err)
	require.True(t, accept)
	require.Equal(t, e.hash, hash, "signed vote must bind the Finish ResponseHash, not a second hash of the request body")

	accept, hash, _, err = VerifyErrorMiss(e.st, 1, nil, e.payload, nil, e.sm)
	require.NoError(t, err)
	require.False(t, accept)
	require.Nil(t, hash)
}

func TestVerifyErrorMiss_PrefersLocalMempoolOverRequestFinishTx(t *testing.T) {
	e := newErrorTimeoutEnv(t)
	other := streamedPayload(t, []string{
		`data: {"error":{"code":500,"message":"different body","type":"InternalServerError"},"id":"devshard-1-1"}`,
		`data: [DONE]`,
	})
	otherHash := payloadSHA256(other)
	tampered := signedErrorFinish(t, e.hosts, 1, 1, 0, otherHash)
	mempool := []*types.DevshardTx{
		{Tx: &types.DevshardTx_FinishInference{FinishInference: e.finish}},
	}
	accept, hash, _, err := VerifyErrorMiss(e.st, 1, marshalFinishTx(t, tampered), e.payload, mempool, e.sm)
	require.NoError(t, err)
	require.True(t, accept, "local mempool Finish is preferred over the request artifact")
	require.Equal(t, e.hash, hash)
}

func TestVerifyErrorMiss_RejectCauses(t *testing.T) {
	e := newErrorTimeoutEnv(t)

	_, _, cause, err := VerifyErrorMiss(e.st, 1, e.finishTx, e.payload, nil, e.sm)
	require.NoError(t, err)
	require.Empty(t, cause, "accept must not set a reject cause")

	_, _, cause, err = VerifyErrorMiss(e.st, 1, nil, e.payload, nil, e.sm)
	require.NoError(t, err)
	require.Equal(t, ErrorTimeoutRejectNoFinishTx, cause)

	_, _, cause, err = VerifyErrorMiss(e.st, 1, e.finishTx, nil, nil, e.sm)
	require.NoError(t, err)
	require.Equal(t, ErrorTimeoutRejectNoPayload, cause)

	other := streamedPayload(t, []string{
		`data: {"error":{"code":500,"message":"different body","type":"InternalServerError"},"id":"devshard-1-1"}`,
		`data: [DONE]`,
	})
	_, _, cause, err = VerifyErrorMiss(e.st, 1, e.finishTx, other, nil, e.sm)
	require.NoError(t, err)
	require.Equal(t, ErrorTimeoutRejectHashMismatch, cause)

	contentThenError := streamedPayload(t, contentThenErrorEvents)
	contentFinish := signedErrorFinish(t, e.hosts, 1, 1, 0, payloadSHA256(contentThenError))
	_, _, cause, err = VerifyErrorMiss(e.st, 1, marshalFinishTx(t, contentFinish), contentThenError, nil, e.sm)
	require.NoError(t, err)
	require.Empty(t, cause, "content-then-error is a miss")

	happy := streamedPayload(t, []string{
		`data: {"id":"devshard-1-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello"}}]}`,
		`data: [DONE]`,
	})
	happyFinish := signedErrorFinish(t, e.hosts, 1, 1, 0, payloadSHA256(happy))
	_, _, cause, err = VerifyErrorMiss(e.st, 1, marshalFinishTx(t, happyFinish), happy, nil, e.sm)
	require.NoError(t, err)
	require.Equal(t, ErrorTimeoutRejectNotErrorBody, cause)

	badSigner := &types.MsgFinishInference{
		InferenceId:  1,
		ResponseHash: e.hash,
		ExecutorSlot: 1,
		EscrowId:     "escrow-1",
	}
	badSigner.ProposerSig = testutil.SignProposerTx(t, e.hosts[0], badSigner)
	_, _, cause, err = VerifyErrorMiss(e.st, 1, marshalFinishTx(t, badSigner), e.payload, nil, e.sm)
	require.NoError(t, err)
	require.Equal(t, ErrorTimeoutRejectSig, cause)
}
