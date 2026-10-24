package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/types"
	"devshard/user"
)

type receiptlessStreamingHost struct {
	inner *user.InProcessClient
}

func (c *receiptlessStreamingHost) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	if req.Payload == nil {
		resp, err := c.inner.Send(ctx, req, stream, receiptHandler)
		if resp != nil {
			resp.Mempool = nil
		}
		return resp, err
	}

	resp, err := c.inner.Host.HandleRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	resp.Receipt = nil
	resp.ConfirmedAt = 0
	resp.ExecutionJob = nil
	resp.Mempool = nil
	if receiptHandler != nil {
		receiptHandler(resp)
	}

	chunk := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
	if _, err := stream.Write(chunk); err != nil {
		return nil, err
	}
	deadline := time.NewTimer(longResponsePerfExemption + time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			if _, err := stream.Write(chunk); err != nil {
				return nil, err
			}
		case <-deadline.C:
			if _, err := io.WriteString(stream, "data: [DONE]\n\n"); err != nil {
				return nil, err
			}
			return resp, nil
		}
	}
}

func TestReceiptlessGatewayAndSettlementRealTime(t *testing.T) {
	if os.Getenv("GONKA_RUN_REALTIME_POC") != "1" {
		t.Skip("set GONKA_RUN_REALTIME_POC=1 to run the 281-second production-timing proof")
	}

	env := setupTestProxy(t, 3, nil, true)
	maliciousHost, ok := env.killables[1].inner.(*user.InProcessClient)
	require.True(t, ok)
	env.killables[1].inner = &receiptlessStreamingHost{inner: maliciousHost}

	params := defaultParams()
	params.MaxTokens = 4096
	initialBalance := env.sm.SnapshotState().Balance
	var output bytes.Buffer
	start := time.Now()
	require.NoError(t, env.proxy.redundancy.RunInference(context.Background(), params, &output, nil))
	require.GreaterOrEqual(t, time.Since(start), longResponsePerfExemption)
	require.Contains(t, output.String(), `"content":"x"`)
	require.Contains(t, output.String(), "data: [DONE]")
	env.proxy.redundancy.waitRaceCleanups()

	rec, ok := env.sm.Inference(1)
	require.True(t, ok)
	require.Equal(t, types.StatusTimedOut, rec.Status)
	require.Positive(t, rec.ReservedCost)
	require.Zero(t, rec.ActualCost)
	require.Equal(t, uint32(1), env.sm.SnapshotState().HostStats[rec.ExecutorSlot].Missed)

	require.NoError(t, env.session.Finalize(context.Background()))
	require.Equal(t, types.PhaseSettlement, env.sm.Phase())
	require.True(t, env.session.HasQuorumAt(env.sm.LatestNonce()))
	after := env.sm.SnapshotState()
	require.Zero(t, after.HostStats[rec.ExecutorSlot].Cost)
	require.Equal(t, uint32(1), after.HostStats[rec.ExecutorSlot].Missed)
	require.LessOrEqual(t, after.Balance, initialBalance)
	require.Less(t, initialBalance-after.Balance, rec.ReservedCost)
	sealed, ok := env.sm.LookupSealedInference(1)
	require.True(t, ok)
	require.Equal(t, types.StatusTimedOut, sealed.Status)
	require.Zero(t, sealed.ActualCost)

	t.Logf("receiptless real-time stream: elapsed=%s nonce=1 slot=%d reserved=%d credited=%d signed_nonce=%d", time.Since(start), rec.ExecutorSlot, rec.ReservedCost, after.HostStats[rec.ExecutorSlot].Cost, env.sm.LatestNonce())
}
