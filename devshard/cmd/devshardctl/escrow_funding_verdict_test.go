package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	// unfundableBalance cannot cover the reservation of even the smallest request, so every escrow
	// holding it refuses to start an inference.
	unfundableBalance = 10
	// feeExceedingBalance covers any reservation these tests make but leaves the escrow unable to pay
	// the per-nonce fee, which is what a drained escrow looks like.
	feeExceedingBalance = 100_000
)

var escrowFundingStreamingCases = []struct {
	name   string
	stream bool
}{{name: "streaming", stream: true}, {name: "non_streaming", stream: false}}

func TestProxyHandsAnUnfundableRequestBackToTheGateway(t *testing.T) {
	for _, testCase := range escrowFundingStreamingCases {
		t.Run(testCase.name, func(t *testing.T) {
			env := setupTestProxyWithBalance(t, 3, nil, true, unfundableBalance)
			verdict := &escrowFundingVerdict{}
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(escrowFundingChatBody(testCase.stream)))
			req = req.WithContext(withEscrowFundingVerdict(req.Context(), verdict))
			rec := httptest.NewRecorder()

			env.proxy.handleChatCompletions(rec, req)

			require.True(t, verdict.refused, "an escrow that could not pay for the request did not hand it back to the gateway")
			require.Empty(t, rec.Body.String(), "an escrow that handed the request back still answered the client")
			require.Empty(t, rec.Result().Header, "an escrow that handed the request back still wrote response headers")
			require.EqualValues(t, 0, env.proxy.session.Nonce(), "a refused reservation consumed a nonce")
		})
	}
}

func TestProxyAnswersTheClientWhenNoPooledRouteCanTryAnotherEscrow(t *testing.T) {
	for _, testCase := range escrowFundingStreamingCases {
		t.Run(testCase.name, func(t *testing.T) {
			env := setupTestProxyWithBalance(t, 3, nil, true, unfundableBalance)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(escrowFundingChatBody(testCase.stream)))
			rec := httptest.NewRecorder()

			env.proxy.handleChatCompletions(rec, req)

			require.Equal(t, http.StatusBadGateway, rec.Code)
			require.Contains(t, rec.Body.String(), "insufficient escrow balance")
		})
	}
}

// The bug this whole path exists to prevent: one request too costly for what is left used to retire a
// healthy escrow, and every replacement carried a new ID the once-only guard never recognised.
func TestProxyKeepsAnEscrowInServiceWhenOneRequestIsTooCostly(t *testing.T) {
	env := setupTestProxyWithBalance(t, 3, nil, true, unfundableBalance)
	var exhaustedFirings atomic.Int64
	env.proxy.redundancy.onBalanceExhausted = func() { exhaustedFirings.Add(1) }
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(escrowFundingChatBody(false)))

	env.proxy.handleChatCompletions(httptest.NewRecorder(), req)

	require.EqualValues(t, 0, exhaustedFirings.Load(), "one request too costly for what is left retired the escrow it was sent to")
}

// The other half of the split: an escrow that cannot pay the per-nonce fee is genuinely spent, so it
// is reported exhausted for replacement and the request still moves to another escrow.
func TestProxyReportsAnEscrowExhaustedWhenItCannotPayTheNonceFee(t *testing.T) {
	env := setupTestProxyWithFeePerNonce(t, 3, nil, true, feeExceedingBalance, feeExceedingBalance)
	var exhaustedFirings atomic.Int64
	env.proxy.redundancy.onBalanceExhausted = func() { exhaustedFirings.Add(1) }
	verdict := &escrowFundingVerdict{}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(escrowFundingChatBody(false)))
	req = req.WithContext(withEscrowFundingVerdict(req.Context(), verdict))

	env.proxy.handleChatCompletions(httptest.NewRecorder(), req)

	require.EqualValues(t, 1, exhaustedFirings.Load(), "an escrow that ran out of funds was not reported exhausted")
	require.True(t, verdict.refused, "a request was failed on a spent escrow instead of being handed back")
}

func TestGatewayPooledChatMovesAnUnfundableRequestToAnotherEscrow(t *testing.T) {
	// Both runtimes idle and equally weighted, so the picker takes them in registration order.
	ctx, abandonRunawayRetries := context.WithCancel(context.Background())
	defer abandonRunawayRetries()
	var attempts atomic.Int64
	refuseOnceThenServe := func(id string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			switch attempts.Add(1) {
			case 1:
				refuseToFundRequest(t, r)
			case 2:
				writeMockenvChatJSON(w, id, mockenvDefaultModel)
			default:
				abandonRunawayRetries()
			}
		}
	}
	first := &gatewayMockRuntime{id: "11", model: mockenvDefaultModel, active: true, handler: refuseOnceThenServe("11")}
	second := &gatewayMockRuntime{id: "22", model: mockenvDefaultModel, active: true, handler: refuseOnceThenServe("22")}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{first, second})

	rec := env.postChat(mockenvChatBody(mockenvDefaultModel, "hello"), withRequestContext(ctx))

	require.Equal(t, http.StatusOK, rec.Code, "a request one escrow could not fund was refused instead of moved")
	require.EqualValues(t, 1, first.calls.Load(), "an escrow that had already refused the request was asked again")
	require.EqualValues(t, 1, second.calls.Load(), "an escrow that had already refused the request was asked again")
	require.Equal(t, "22", rec.Header().Get("X-Devshard-ID"), "the response names an escrow that did not serve it")
	require.Contains(t, rec.Body.String(), "from 22")
	requireEveryEscrowStillServing(t, env, 2)
}

// The gateway's header and context must survive the trip through a real proxy, which is where the
// refusal is raised.
func TestGatewayPooledChatMovesARealProxyRefusalToAnotherEscrow(t *testing.T) {
	for _, testCase := range escrowFundingStreamingCases {
		t.Run(testCase.name, func(t *testing.T) {
			proxyEnv := setupTestProxyWithBalance(t, 3, nil, true, unfundableBalance)
			proxyEnv.proxy.model = mockenvDefaultModel
			// Both runtimes idle and equally weighted, so the picker takes the unfunded one first.
			unfunded := &gatewayMockRuntime{id: "11", model: mockenvDefaultModel, active: true, handler: newRuntimeMux(proxyEnv.proxy).ServeHTTP}
			funded := &gatewayMockRuntime{id: "22", model: mockenvDefaultModel, active: true, handler: func(w http.ResponseWriter, r *http.Request) {
				if testCase.stream {
					writeMockenvChatSSE(w, "22", mockenvDefaultModel)
					return
				}
				writeMockenvChatJSON(w, "22", mockenvDefaultModel)
			}}
			env := newGatewayMockEnv(t, []*gatewayMockRuntime{unfunded, funded})

			rec := env.postChat(escrowFundingChatBodyForModel(mockenvDefaultModel, testCase.stream))

			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, "22", rec.Header().Get("X-Devshard-ID"))
			require.EqualValues(t, 1, unfunded.calls.Load(), "the escrow that could not fund the request was never asked")
			require.Contains(t, rec.Body.String(), "from 22")
			require.EqualValues(t, 0, proxyEnv.proxy.session.Nonce(), "a refused reservation consumed a nonce")
			requireEveryEscrowStillServing(t, env, 2)
		})
	}
}

func TestGatewayPooledChatReportsRetryAfterOnceNoEscrowCanFundTheRequest(t *testing.T) {
	ctx, abandonRunawayRetries := context.WithCancel(context.Background())
	defer abandonRunawayRetries()
	var attempts atomic.Int64
	refuseToFund := func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) > 2 {
			abandonRunawayRetries()
			return
		}
		refuseToFundRequest(t, r)
	}
	first := &gatewayMockRuntime{id: "11", model: mockenvDefaultModel, active: true, handler: refuseToFund}
	second := &gatewayMockRuntime{id: "22", model: mockenvDefaultModel, active: true, handler: refuseToFund}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{first, second})

	rec := env.postChat(mockenvChatBody(mockenvDefaultModel, "hello"), withRequestContext(ctx))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "10", rec.Header().Get("Retry-After"))
	require.Contains(t, rec.Body.String(), "no escrow can fund this request (2 refused)")
	require.Contains(t, rec.Body.String(), "cannot_fund_request=2", "the refusal does not say which escrows were skipped and why")
	require.Empty(t, rec.Header().Get("X-Devshard-ID"), "the refusal names an escrow that never served the request")
	require.EqualValues(t, 1, first.calls.Load(), "an escrow that had already refused the request was asked again")
	require.EqualValues(t, 1, second.calls.Load(), "an escrow that had already refused the request was asked again")
	requireEveryEscrowStillServing(t, env, 2)
}

func TestGatewayPooledChatStopsMovingTheRequestOnceTheClientIsGone(t *testing.T) {
	ctx, clientGone := context.WithCancel(context.Background())
	defer clientGone()
	first := &gatewayMockRuntime{id: "11", model: mockenvDefaultModel, active: true, handler: func(w http.ResponseWriter, r *http.Request) {
		refuseToFundRequest(t, r)
		clientGone()
	}}
	second := &gatewayMockRuntime{id: "22", model: mockenvDefaultModel, active: true}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{first, second})

	env.postChat(mockenvChatBody(mockenvDefaultModel, "hello"), withRequestContext(ctx))

	require.EqualValues(t, 1, first.calls.Load())
	require.EqualValues(t, 0, second.calls.Load(), "the request was moved to another escrow after the client had gone")
	requireEveryEscrowStillServing(t, env, 2)
}

func refuseToFundRequest(t *testing.T, r *http.Request) {
	t.Helper()
	verdict, recorded := escrowFundingVerdictFromContext(r.Context())
	require.True(t, recorded, "the pooled route was not waiting for an escrow's verdict")
	verdict.refused = true
}

// A refused attempt that keeps its reservation leaves the escrow looking busy forever, which also
// blocks the settle/retire drain; refusing one request must not take the escrow out of service.
func requireEveryEscrowStillServing(t *testing.T, env *gatewayMockEnv, expectedEscrows int) {
	t.Helper()
	require.Len(t, env.gateway.runtimeOrder, expectedEscrows, "an escrow was retired for refusing to fund one request")
	for _, rt := range env.gateway.runtimeOrder {
		require.Zero(t, rt.activeUserRequests.Load(), "escrow %s kept an active request after the response", rt.id)
		require.Zero(t, rt.reservedTokens.Load(), "escrow %s kept reserved tokens after the response", rt.id)
		require.True(t, rt.active.Load(), "escrow %s was taken out of service for refusing to fund one request", rt.id)
	}
}

func escrowFundingChatBody(stream bool) string {
	return fmt.Sprintf(`{"messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream)
}

func escrowFundingChatBodyForModel(model string, stream bool) string {
	return fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"stream":%t}`, model, stream)
}
