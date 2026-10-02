package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"common/completionapi"
	"devshard"
	"devshard/host"
	"devshard/stub"
	"devshard/types"
	"devshard/user"
)

const outOfVocabRejection = `{"error":{"code":400,"message":"token_id 999999999 in logit_bias contains out-of-vocab token id","type":"BadRequestError"}}`

type clientFaultEngine struct {
	calls atomic.Int32
}

func (e *clientFaultEngine) Execute(_ context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	e.calls.Add(1)
	processor := completionapi.NewExecutorResponseProcessor(fmt.Sprintf("devshard-%s-%d", req.EscrowID, req.InferenceID), false)
	var forwarded []string
	for _, line := range []string{completionapi.DataPrefix + outOfVocabRejection, "data: [DONE]"} {
		out, err := processor.ProcessStreamedResponse(line)
		if err != nil {
			return nil, err
		}
		forwarded = append(forwarded, out)
	}
	if req.ResponseWriter != nil {
		for _, line := range forwarded {
			fmt.Fprintf(req.ResponseWriter, "%s\n\n", line)
		}
		if flusher, ok := req.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	stored, err := processor.GetResponseBytes()
	if err != nil {
		return nil, err
	}
	served, err := processor.GetServedHash()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(stored)
	return &devshard.ExecuteResult{ResponseHash: sum[:], ServedHash: served[:], ResponseBody: stored}, nil
}

type relayingInProcessClient struct {
	*user.InProcessClient
}

func (c relayingInProcessClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	resp, err := c.Host.HandleRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	if receiptHandler != nil {
		receiptHandler(resp)
	}
	if resp.ExecutionJob != nil {
		result, err := c.Host.RunExecution(ctx, resp.ExecutionJob)
		if err != nil {
			return nil, err
		}
		var envelope completionapi.SerializedStreamedResponse
		if err := json.Unmarshal(result.ResponseBody, &envelope); err != nil {
			return nil, err
		}
		for _, line := range envelope.Events {
			if _, err := fmt.Fprintf(stream, "%s\n\n", line); err != nil {
				return nil, err
			}
		}
		resp.Mempool = c.Host.MempoolTxs()
	}
	return resp, nil
}

func runClientFaultRace(t *testing.T, engines []devshard.InferenceEngine) (*testProxyEnv, error, string) {
	t.Helper()
	withRedundancySpeedPolicyForProxyTest(t, RedundancySpeedPolicyLegacy)
	shortRefusalWindow(t)
	env := setupTestProxy(t, len(engines), engines, true)
	for idx, engine := range engines {
		if _, refuses := engine.(*clientFaultEngine); refuses {
			env.killables[idx].inner = relayingInProcessClient{InProcessClient: env.killables[idx].inner.(*user.InProcessClient)}
		}
	}
	cleanupFinished := raceCleanupFinished(env.proxy.redundancy)

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil)
	requireClosedWithin(t, cleanupFinished, "the background cleanup never finished")
	return env, err, buf.String()
}

func TestRunInference_FabricatedClientFaultIsAMissWhenAnotherHostServes(t *testing.T) {
	refusing := &clientFaultEngine{}
	env, err, served := runClientFaultRace(t, []devshard.InferenceEngine{stub.NewInferenceEngine(), refusing, stub.NewInferenceEngine()})

	require.NoError(t, err)
	require.Contains(t, served, `"choices"`)
	require.Equal(t, int32(1), refusing.calls.Load())
	require.Equal(t, uint32(1), missesForSlot(t, env, 1))
	refused, found := env.sm.GetInference(1)
	require.True(t, found)
	require.Equal(t, types.StatusTimedOut, refused.Status)

	var sibling uint64
	for idx, verifier := range env.verifiers {
		if idx == 1 {
			continue
		}
		if proof := verifier.errorMissProof.Load(); proof != 0 {
			sibling = proof
		}
	}
	require.NotZero(t, sibling, "the miss must name the attempt that served the prompt")
	servingRecord, found := env.sm.GetInference(sibling)
	require.True(t, found)
	require.Equal(t, refused.PromptHash, servingRecord.PromptHash)
	require.Positive(t, servingRecord.OutputTokens)
}

func TestRunInference_ClientFaultEveryHostRejectsChargesNoHost(t *testing.T) {
	engines := []*clientFaultEngine{{}, {}, {}}
	env, err, _ := runClientFaultRace(t, []devshard.InferenceEngine{engines[0], engines[1], engines[2]})

	var hostErr *hostApplicationError
	require.ErrorAs(t, err, &hostErr)
	require.Equal(t, http.StatusBadRequest, hostErr.statusCode())
	require.True(t, hostErr.confirmedByHosts)
	calls := engines[0].calls.Load() + engines[1].calls.Load() + engines[2].calls.Load()
	require.Equal(t, int32(2), calls, "the request stops once two hosts agree it is the client's fault")
	for slot := range engines {
		require.Zero(t, missesForSlot(t, env, slot), "slot %d", slot)
	}
	for _, verifier := range env.verifiers {
		require.Zero(t, verifier.errorMissProof.Load())
	}
}
