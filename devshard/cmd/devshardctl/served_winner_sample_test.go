package main

import (
	"bytes"
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/types"
	"devshard/user"
)

type finishLaterClient struct {
	respond func(nonce uint64) *host.HostResponse
}

func (c *finishLaterClient) Send(_ context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	if receiptHandler != nil {
		receiptHandler(&host.HostResponse{})
	}
	if stream != nil {
		_, _ = io.WriteString(stream, `data: {"choices":[{"delta":{"content":"x"}}]}`+"\n\n")
	}
	return c.respond(req.Nonce), nil
}

// Test flow:
// 1. The only host streams a whole answer and returns a signed receipt, but its finish is not in the reply.
// 2. RunInference serves the answer, and the race settles while the nonce is still open.
// 3. The finish arrives a moment later and closes the nonce.
// 4. The host ends with one responsive sample, no failure, and Decide does not hedge it as primary_unresponsive.
// 5. The request counts no hidden failure, and the attempt is a successful user-visible win with no attempt failure.
func TestServedWinnerWhoseFinishArrivesLateKeepsAResponsiveSample(t *testing.T) {
	client := &finishLaterClient{}
	env := setupTestProxyWithClients(t, []user.HostClient{client})
	var servedNonce atomic.Uint64
	client.respond = func(nonce uint64) *host.HostResponse {
		if _, ok := env.session.StateMachine().Inference(nonce); !ok {
			return &host.HostResponse{Nonce: nonce}
		}
		servedNonce.CompareAndSwap(0, nonce)
		resp := applicableResponse(t, env, nonce)
		resp.Mempool = nil
		return resp
	}
	redundancy := env.proxy.redundancy
	metrics := NewDevshardMetrics()
	redundancy.metrics = metrics
	participantKey := env.session.HostParticipantKey(0)
	settled := raceCleanupFinished(redundancy)

	var served bytes.Buffer
	require.NoError(t, redundancy.RunInference(context.Background(), defaultParams(), &served, nil))
	require.Contains(t, served.String(), `"content":"x"`)
	requireClosedWithin(t, settled, "the race never settled")
	nonce := servedNonce.Load()
	require.NotZero(t, nonce)
	require.False(t, env.session.IsNonceFinished(nonce))

	require.NoError(t, env.session.ProcessResponse(0, &host.HostResponse{
		Nonce:   nonce,
		Mempool: []*types.DevshardTx{applicableFinishTx(t, env, nonce)},
	}, nonce))
	require.True(t, env.session.IsNonceFinished(nonce))

	require.Eventually(t, func() bool {
		return redundancy.perf.StatsForParticipant(participantKey).TotalSamples == 1
	}, testWaitLimit, 20*time.Millisecond)
	stats := redundancy.perf.StatsForParticipant(participantKey)
	require.Zero(t, stats.FailureSamples)
	require.Equal(t, 1.0, stats.ResponsiveRate)
	require.NotEqual(t, "primary_unresponsive", redundancy.Decide(0, 100).Reason)
	requireServedWinnerMetrics(t, metrics, participantKey, 1, 0)
}

// Test flow:
// 1. The only host streams a whole answer twice and never sends a finish for either nonce.
// 2. Both answers are served, and once both races settle the attempts are successful user-visible wins.
// 3. Before the deadline the host has no sample and no strike, and the requests count no hidden failure.
// 4. Once each nonce's execution deadline passes, the host has two failure samples and the stalled-winner strike.
// 5. Both requests then count one not_finished hidden failure each, and the attempts stay successful user-visible wins.
func TestServedWinnerWhoseNonceNeverFinishesIsStruckAtTheDeadline(t *testing.T) {
	env := setupTestProxyWithClients(t, []user.HostClient{streamContentWithoutFinishClient{}})
	limiter := NewParticipantRequestLimiter(10, 10)
	redundancy := env.proxy.redundancy
	redundancy.participantLimiter = limiter
	metrics := NewDevshardMetrics()
	redundancy.metrics = metrics
	participantKey := env.session.HostParticipantKey(0)

	for i := 0; i < 2; i++ {
		var served bytes.Buffer
		require.NoError(t, redundancy.RunInference(context.Background(), defaultParams(), &served, nil))
		require.Contains(t, served.String(), `"content":"x"`)
	}
	require.Eventually(t, func() bool {
		return servedWinnerWins(t, metrics, participantKey) == 2
	}, testWaitLimit, 20*time.Millisecond)
	require.Zero(t, redundancy.perf.StatsForParticipant(participantKey).TotalSamples)
	require.False(t, limiter.IsShadowQuarantined(participantKey))
	requireServedWinnerMetrics(t, metrics, participantKey, 2, 0)

	require.Eventually(t, func() bool {
		return limiter.IsShadowQuarantined(participantKey) && hiddenFailureCount(t, metrics, map[string]string{"reason": "not_finished"}) == 2
	}, testWaitLimit, 20*time.Millisecond)
	stats := redundancy.perf.StatsForParticipant(participantKey)
	require.Equal(t, 2, stats.TotalSamples)
	require.Equal(t, 2, stats.FailureSamples)
	require.Equal(t, "primary_unresponsive", redundancy.Decide(0, 100).Reason)
	requireServedWinnerMetrics(t, metrics, participantKey, 2, 2)
}

func requireServedWinnerMetrics(t *testing.T, metrics *DevshardMetrics, participantKey string, wins, notFinished float64) {
	t.Helper()
	families, err := metrics.registry.Gather()
	require.NoError(t, err)
	require.Equal(t, wins, servedWinnerWins(t, metrics, participantKey))
	requireMetricFamilyAbsent(t, families, "devshard_gateway_attempt_failures_total")
	require.Equal(t, notFinished, hiddenFailureCount(t, metrics, map[string]string{"reason": "not_finished"}))
	require.Equal(t, notFinished, hiddenFailureCount(t, metrics, nil))
}

func servedWinnerWins(t *testing.T, metrics *DevshardMetrics, participantKey string) float64 {
	t.Helper()
	families, err := metrics.registry.Gather()
	require.NoError(t, err)
	return metricCounterValueOrZero(families, "devshard_gateway_attempts_terminal_total", map[string]string{
		"participant_key": participantKey,
		"model":           "llama",
		"role":            "primary",
		"outcome":         "success",
		"visibility":      "user_visible_winner",
	})
}

func hiddenFailureCount(t *testing.T, metrics *DevshardMetrics, labels map[string]string) float64 {
	t.Helper()
	families, err := metrics.registry.Gather()
	require.NoError(t, err)
	total := 0.0
	for _, family := range families {
		if family.GetName() != "devshard_gateway_user_requests_with_hidden_failure_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			if metricLabelsContain(metric, labels) {
				total += metric.GetCounter().GetValue()
			}
		}
	}
	return total
}
