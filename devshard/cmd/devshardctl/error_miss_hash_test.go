package main

import (
	"crypto/sha256"
	"strings"
	"testing"

	"common/completionapi"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const (
	errorMissInferenceID = uint64(1)
	errorMissEscrowID    = "escrow-1"
	errorMissExecutor    = uint32(1)
)

var errorMissStream = []string{
	`data: {"id":"seed","object":"chat.completion.chunk","created":1,"model":"m","prompt_token_ids":[11,22],` +
		`"choices":[{"index":0,"delta":{"role":"assistant"},"logprobs":null}]}`,
	`data: {"error":{"code":500,"message":"EngineCore encountered an issue","type":"InternalServerError"},"id":"seed"}`,
	`data: [DONE]`,
}

func TestErrorMissProofRehashesOnlyWhenTheExecutorForwardsWhatItStored(t *testing.T) {
	for _, testCase := range []struct {
		name                  string
		forwardStoredResponse bool
		wantAccept            bool
		wantRejectCause       string
	}{
		{name: "optimization on", forwardStoredResponse: false, wantRejectCause: host.ErrorTimeoutRejectHashMismatch},
		{name: "optimization off", forwardStoredResponse: true, wantAccept: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			wire, committedHash := executorAnswer(t, testCase.forwardStoredResponse)

			_, responsePayload := errorMissArtifacts(&inflight{errorStreamLines: ssePayloadDataLines(wire)}, nil)
			require.NotEmpty(t, responsePayload, "the gateway rebuilt no payload from the wire")

			stateMachine, hosts := errorMissStateMachine(t)
			finishTx := signedErrorFinishTx(t, hosts, committedHash)

			accept, _, rejectCause, err := host.VerifyErrorMiss(
				startedInferenceState(), errorMissInferenceID, finishTx, responsePayload, nil, stateMachine)
			require.NoError(t, err)
			require.Equal(t, testCase.wantAccept, accept)
			require.Equal(t, testCase.wantRejectCause, rejectCause)
		})
	}
}

func executorAnswer(t *testing.T, forwardStoredResponse bool) (wire []byte, committedHash []byte) {
	t.Helper()
	processor := completionapi.NewExecutorResponseProcessor("devshard-escrow-1-1", false)
	processor.SetLogprobsOptimization(nil, !forwardStoredResponse)

	var forwarded strings.Builder
	for _, event := range errorMissStream {
		relayed, err := processor.ProcessStreamedResponse(event)
		require.NoError(t, err)
		forwarded.WriteString(relayed + "\n\n")
	}

	stored, err := processor.GetResponseBytes()
	require.NoError(t, err)
	sum := sha256.Sum256(stored)
	return []byte(forwarded.String()), sum[:]
}

func errorMissStateMachine(t *testing.T) (*state.StateMachine, []*signing.Secp256k1Signer) {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	config := testutil.DefaultConfig(len(hosts))
	group := testutil.MakeGroup(hosts)
	stateMachine, err := state.NewStateMachine(
		errorMissEscrowID, config, group, 10000, user.Address(), signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, errorMissEscrowID, user.Address(), config, group, 10000),
	)
	require.NoError(t, err)
	return stateMachine, hosts
}

func signedErrorFinishTx(t *testing.T, hosts []*signing.Secp256k1Signer, responseHash []byte) []byte {
	t.Helper()
	msg := &types.MsgFinishInference{
		InferenceId:  errorMissInferenceID,
		ResponseHash: responseHash, ServedHash: testutil.TestServedHash,
		ExecutorSlot: errorMissExecutor,
		EscrowId:     errorMissEscrowID,
	}
	msg.ProposerSig = testutil.SignProposerTx(t, hosts[errorMissExecutor], msg)
	encoded, err := proto.Marshal(&types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}})
	require.NoError(t, err)
	return encoded
}

func startedInferenceState() types.EscrowState {
	return types.EscrowState{
		EscrowID: errorMissEscrowID,
		Config:   types.SessionConfig{TokenPrice: 1, VoteThreshold: 1, RefusalTimeout: 60, ExecutionTimeout: 1200},
		Inferences: map[uint64]*types.InferenceRecord{
			errorMissInferenceID: {
				Status:       types.StatusStarted,
				ExecutorSlot: errorMissExecutor,
				ReservedCost: 150,
				StartedAt:    1000,
				ConfirmedAt:  1000,
			},
		},
		HostStats: map[uint32]*types.HostStats{0: {}, 1: {}},
		Balance:   10000,
	}
}
