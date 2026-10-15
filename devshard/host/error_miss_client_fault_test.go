package host

import (
	"testing"

	"github.com/stretchr/testify/require"

	"common/completionapi"

	"devshard/internal/testutil"
	"devshard/types"
)

func TestVerifyErrorMiss_ClientFaultIsNotAMiss(t *testing.T) {
	clientFault := `data: {"error":{"code":400,"message":"token_id 999999999 in logit_bias contains out-of-vocab token id","type":"BadRequestError"},"id":"devshard-1-1"}`
	for _, tc := range []struct {
		name         string
		lines        []string
		outputTokens uint64
		accept       bool
	}{
		{name: "unbilled client fault", lines: []string{clientFault}},
		{name: "billed client fault", lines: []string{clientFault}, outputTokens: 4096, accept: true},
		{name: "output before the client fault", lines: []string{`data: {"id":"devshard-1-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello"}}]}`, clientFault}, accept: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newErrorTimeoutEnv(t)
			payload := streamedPayload(t, append(tc.lines, `data: [DONE]`))
			finish := signedErrorFinish(t, e.hosts, 1, 1, tc.outputTokens, payloadSHA256(payload))

			accept, _, cause, err := VerifyErrorMiss(e.st, 1, marshalFinishTx(t, finish), payload, nil, e.sm)

			require.NoError(t, err)
			require.Equal(t, tc.accept, accept, "reject cause: %s", cause)
			if !tc.accept {
				require.Equal(t, ErrorTimeoutRejectClientFault, cause)
			}
		})
	}
}

func TestVerifyErrorMiss_FinishedRecordBindsTheAppliedFinish(t *testing.T) {
	payload := streamedPayload(t, []string{`data: {"error":{"code":400,"message":"bad"}}`, `data: [DONE]`})
	for _, tc := range []struct {
		name           string
		appliedOutput  uint64
		sentOutput     uint64
		mempoolApplied bool
		otherServed    bool
		accept         bool
		cause          string
	}{
		{name: "applied billed finish", appliedOutput: 100, sentOutput: 100, accept: true},
		{name: "zero finish against billed record", appliedOutput: 100, cause: ErrorTimeoutRejectHashMismatch},
		{name: "zero finish with the applied one in the mempool", appliedOutput: 100, mempoolApplied: true, accept: true},
		{name: "finish with another served hash", sentOutput: 0, otherServed: true, cause: ErrorTimeoutRejectHashMismatch},
		{name: "applied unbilled finish", cause: ErrorTimeoutRejectClientFault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newErrorTimeoutEnv(t)
			applied := signedErrorFinish(t, e.hosts, 1, 1, tc.appliedOutput, payloadSHA256(payload))
			rec := e.st.Inferences[1]
			rec.Status = types.StatusFinished
			rec.ResponseHash = applied.ResponseHash
			rec.ServedHash = applied.ServedHash
			rec.OutputTokens = tc.appliedOutput
			sent := signedErrorFinish(t, e.hosts, 1, 1, tc.sentOutput, payloadSHA256(payload))
			if tc.otherServed {
				sent.ServedHash = payloadSHA256([]byte("another view"))
				sent.ProposerSig = nil
				sent.ProposerSig = testutil.SignProposerTx(t, e.hosts[1], sent)
			}
			var mempool []*types.DevshardTx
			if tc.mempoolApplied {
				mempool = append(mempool, &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: applied}})
			}

			accept, _, cause, err := VerifyErrorMiss(e.st, 1, marshalFinishTx(t, sent), payload, mempool, e.sm)

			require.NoError(t, err)
			require.Equal(t, tc.accept, accept, "reject cause: %s", cause)
			require.Equal(t, tc.cause, cause)
		})
	}
}

func TestVerifyErrorMiss_ClientFaultClassificationByView(t *testing.T) {
	for _, tc := range []struct {
		name, line                 string
		acceptStored, acceptServed bool
	}{
		{name: "role", line: `data: {"choices":[{"delta":{"role":"assistant"},"logprobs":null}]}`},
		{name: "hidden output", line: `data: {"choices":[{"delta":{},"logprobs":{"content":[{"token":"42"}]}}]}`, acceptStored: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newErrorTimeoutEnv(t)
			p := completionapi.NewExecutorResponseProcessor("devshard-1-1", false)
			p.SetLogprobsOptimization(nil, true)
			var lines []string
			for _, line := range []string{tc.line, `data: {"error":{"code":400,"message":"bad"}}`, `data: [DONE]`} {
				forwarded, err := p.ProcessStreamedResponse(line)
				require.NoError(t, err)
				lines = append(lines, forwarded)
			}
			stored, err := p.GetResponseBytes()
			require.NoError(t, err)
			servedHash, err := p.GetServedHash()
			require.NoError(t, err)
			finish := signedErrorFinish(t, e.hosts, 1, 1, 0, payloadSHA256(stored))
			finish.ServedHash = servedHash[:]
			finish.ProposerSig = nil
			finish.ProposerSig = testutil.SignProposerTx(t, e.hosts[1], finish)
			for _, view := range []struct {
				payload []byte
				accept  bool
			}{{payload: stored, accept: tc.acceptStored}, {payload: streamedPayload(t, lines), accept: tc.acceptServed}} {
				accept, _, cause, err := VerifyErrorMiss(e.st, 1, marshalFinishTx(t, finish), view.payload, nil, e.sm)
				require.NoError(t, err)
				require.Equal(t, view.accept, accept, cause)
			}
		})
	}
}

func TestVerifyErrorMiss_ClientFaultIsAMissOnlyWithAServedSibling(t *testing.T) {
	prompt := []byte("prompt-hash")
	served := func() *types.InferenceRecord {
		return &types.InferenceRecord{Status: types.StatusFinished, ExecutorSlot: 0, PromptHash: prompt, OutputTokens: 12}
	}
	for _, tc := range []struct {
		name      string
		siblingID uint64
		sibling   func() *types.InferenceRecord
		accept    bool
	}{
		{name: "finished sibling served the prompt", siblingID: 2, sibling: served, accept: true},
		{name: "validated sibling served the prompt", siblingID: 2, sibling: func() *types.InferenceRecord {
			rec := served()
			rec.Status = types.StatusValidated
			return rec
		}, accept: true},
		{name: "same executor slot served it elsewhere", siblingID: 2, sibling: func() *types.InferenceRecord {
			rec := served()
			rec.ExecutorSlot = 1
			return rec
		}, accept: true},
		{name: "no sibling"},
		{name: "unknown sibling", siblingID: 9},
		{name: "self as sibling", siblingID: 1},
		{name: "different prompt", siblingID: 2, sibling: func() *types.InferenceRecord {
			rec := served()
			rec.PromptHash = []byte("other")
			return rec
		}},
		{name: "sibling without output", siblingID: 2, sibling: func() *types.InferenceRecord {
			rec := served()
			rec.OutputTokens = 0
			return rec
		}},
		{name: "invalidated sibling", siblingID: 2, sibling: func() *types.InferenceRecord {
			rec := served()
			rec.Status = types.StatusInvalidated
			return rec
		}},
		{name: "challenged sibling", siblingID: 2, sibling: func() *types.InferenceRecord {
			rec := served()
			rec.Status = types.StatusChallenged
			return rec
		}},
		{name: "unfinished sibling", siblingID: 2, sibling: func() *types.InferenceRecord {
			rec := served()
			rec.Status = types.StatusStarted
			return rec
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newErrorTimeoutEnv(t)
			e.st.Inferences[1].PromptHash = prompt
			if tc.sibling != nil {
				e.st.Inferences[2] = tc.sibling()
			}
			payload := streamedPayload(t, []string{`data: {"error":{"code":400,"message":"bad","type":"BadRequestError"},"id":"devshard-1-1"}`, `data: [DONE]`})
			finish := signedErrorFinish(t, e.hosts, 1, 1, 0, payloadSHA256(payload))

			accept, hash, cause, err := VerifyErrorMissWithSibling(e.st, 1, marshalFinishTx(t, finish), payload, tc.siblingID, nil, e.sm)

			require.NoError(t, err)
			require.Equal(t, tc.accept, accept, "reject cause: %s", cause)
			if tc.accept {
				require.Equal(t, finish.ResponseHash, hash)
			} else {
				require.Equal(t, ErrorTimeoutRejectClientFault, cause)
			}
		})
	}
}
