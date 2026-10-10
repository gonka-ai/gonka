package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/types"
)

func inflightWithSignedFinish(errorLine string) *inflight {
	return &inflight{
		nonce:            7,
		errorSource:      "error.BadRequestError",
		errorTerminal:    true,
		errorStreamLines: []string{errorLine, `data: [DONE]`},
		resp: &host.HostResponse{
			Mempool: []*types.DevshardTx{
				{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{ServedHash: testutil.TestServedHash, InferenceId: 7}}},
			},
		},
	}
}

func TestErrorMissRunnable_WithoutASession(t *testing.T) {
	clientFault := `data: {"error":{"code":400,"message":"token_id 999999999 in logit_bias contains out-of-vocab token id","type":"BadRequestError"}}`
	for _, tc := range []struct {
		name         string
		inf          *inflight
		billed       uint64
		wantRunnable bool
		wantReason   string
	}{
		{name: "unbilled bad request", inf: inflightWithSignedFinish(clientFault), wantReason: "client_fault"},
		{name: "unbilled unprocessable entity", inf: inflightWithSignedFinish(`data: {"error":{"code":422,"message":"bad"}}`), wantReason: "client_fault"},
		{name: "billed client fault", inf: inflightWithSignedFinish(clientFault), billed: 4096, wantRunnable: true},
		{name: "engine fault", inf: inflightWithSignedFinish(auditErrLine), wantRunnable: true},
		{name: "no finish", inf: &inflight{nonce: 7, errorSource: "error.InternalServerError", errorTerminal: true, errorStreamLines: []string{auditErrLine}}, wantReason: "no_finish_artifact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.billed > 0 {
				tc.inf.resp.Mempool[0].GetFinishInference().OutputTokens = tc.billed
			}
			require.Equal(t, tc.wantRunnable, errorMissRunnable(tc.inf, nil))
			if !tc.wantRunnable {
				require.Equal(t, tc.wantReason, errorMissSkipReason(tc.inf, nil))
			}
		})
	}
}

func TestErrorMissRunnable_AlternativeZeroFinishCannotHideAppliedBilling(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	params := defaultParams()
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)
	nonce, execIdx := prepared.Nonce(), prepared.HostIdx()
	signer := env.verifiers[execIdx].signer
	errorLine := `data: {"error":{"code":400,"message":"bad","type":"BadRequestError"}}`
	inf := &inflight{
		nonce:            nonce,
		errorTerminal:    true,
		errorStreamLines: []string{errorLine, "data: [DONE]"},
	}
	_, payload := errorMissArtifacts(inf, nil)
	sum := sha256.Sum256(payload)
	applied := &types.MsgFinishInference{
		InferenceId: nonce, EscrowId: "escrow-proxy", ExecutorSlot: uint32(execIdx),
		ResponseHash: sum[:], ServedHash: sum[:], OutputTokens: 4096,
	}
	applied.ProposerSig = testutil.SignProposerTx(t, signer, applied)
	appliedTx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: applied}}
	receipt := testutil.SignExecutorReceipt(t, signer, "escrow-proxy", nonce, testutil.TestPromptHash[:], params.Model,
		params.InputLength, params.MaxTokens, params.StartedAt, params.StartedAt+1)
	require.NoError(t, env.session.ProcessResponse(execIdx, &host.HostResponse{
		Receipt: receipt, ConfirmedAt: params.StartedAt + 1, Mempool: []*types.DevshardTx{appliedTx},
	}, nonce))
	require.NoError(t, env.session.SendPendingDiff(context.Background()))
	rec, found := env.sm.GetInference(nonce)
	require.True(t, found)
	require.Equal(t, types.StatusFinished, rec.Status)
	require.Equal(t, uint64(4096), rec.OutputTokens)

	alternativeTx := proto.Clone(appliedTx).(*types.DevshardTx)
	alternative := alternativeTx.GetFinishInference()
	alternative.OutputTokens = 0
	alternative.ProposerSig = testutil.SignProposerTx(t, signer, alternative)
	inf.resp = &host.HostResponse{Mempool: []*types.DevshardTx{alternativeTx}}
	finishTx, _ := errorMissArtifacts(inf, env.session)
	require.Equal(t, uint64(4096), host.DecodeFinishTx(finishTx).OutputTokens)
	require.True(t, errorMissRunnable(inf, env.session))
	require.True(t, shouldRunHandleTimeout(inf, env.session))
}

func appliedErrorFinishInflight(t *testing.T, env *testProxyEnv, errorLine string, outputTokens uint64) *inflight {
	t.Helper()
	params := defaultParams()
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)
	nonce, execIdx := prepared.Nonce(), prepared.HostIdx()
	signer := env.verifiers[execIdx].signer
	inf := &inflight{
		nonce:            nonce,
		hostIdx:          execIdx,
		errorSource:      "error.BadRequestError",
		errorTerminal:    true,
		errorStreamLines: []string{errorLine, "data: [DONE]"},
	}
	_, payload := errorMissArtifacts(inf, nil)
	sum := sha256.Sum256(payload)
	applied := &types.MsgFinishInference{
		InferenceId: nonce, EscrowId: "escrow-proxy", ExecutorSlot: uint32(execIdx),
		ResponseHash: sum[:], ServedHash: sum[:], OutputTokens: outputTokens,
	}
	applied.ProposerSig = testutil.SignProposerTx(t, signer, applied)
	appliedTx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: applied}}
	receipt := testutil.SignExecutorReceipt(t, signer, "escrow-proxy", nonce, testutil.TestPromptHash[:], params.Model,
		params.InputLength, params.MaxTokens, params.StartedAt, params.StartedAt+1)
	inf.resp = &host.HostResponse{Receipt: receipt, ConfirmedAt: params.StartedAt + 1, Mempool: []*types.DevshardTx{appliedTx}}
	require.NoError(t, env.session.ProcessResponse(execIdx, inf.resp, nonce))
	require.NoError(t, env.session.SendPendingDiff(context.Background()))
	require.True(t, env.session.IsNonceFinished(nonce))
	return inf
}

func TestAttemptCountsAsSuccessfulForPerf_UnbilledClientFaultIsNotResponsive(t *testing.T) {
	clientFault := `data: {"error":{"code":400,"message":"bad","type":"BadRequestError"}}`
	for _, tc := range []struct {
		name         string
		line         string
		outputTokens uint64
		want         bool
	}{
		{name: "unbilled client fault", line: clientFault},
		{name: "billed client fault", line: clientFault, outputTokens: 7, want: true},
		{name: "engine fault", line: auditErrLine, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTestProxy(t, 3, nil, true)
			inf := appliedErrorFinishInflight(t, env, tc.line, tc.outputTokens)
			require.Equal(t, tc.want, attemptCountsAsSuccessfulForPerf(inf, env.session))
		})
	}
}

func TestRaceGroup_DeterministicRejectionNeedsTwoHosts(t *testing.T) {
	ctx := context.Background()
	var sink bytes.Buffer
	race := newRaceGroup(ctx, ctx, "escrow-x", &sink)

	race.markDeterministicallyRejected("host-A")
	require.False(t, race.isDeterministicallyRejected())
	race.markDeterministicallyRejected("host-A")
	require.False(t, race.isDeterministicallyRejected())
	race.markDeterministicallyRejected("host-B")
	require.True(t, race.isDeterministicallyRejected())
}

func TestClientVisibleError_ConfirmedOnlyByTwoRejectingHosts(t *testing.T) {
	rejection := func(hostID string, nonce uint64) *inflight {
		return &inflight{hostID: hostID, nonce: nonce, errorSource: "error.BadRequestError", errorCode: "400", errorType: "BadRequestError", errorMessage: "bad request"}
	}
	for _, tc := range []struct {
		name     string
		attempts []*inflight
		want     bool
	}{
		{name: "one host", attempts: []*inflight{rejection("host-A", 1)}},
		{name: "same host twice", attempts: []*inflight{rejection("host-A", 1), rejection("host-A", 2)}},
		{name: "two hosts", attempts: []*inflight{rejection("host-A", 1), rejection("host-B", 2)}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hostErr *hostApplicationError
			require.ErrorAs(t, clientVisibleAllAttemptsFailedError(tc.attempts, 0), &hostErr)
			require.Equal(t, tc.want, hostErr.confirmedByHosts)
		})
	}
}
