package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/types"
)

func TestAwaitRace_WinnerInvalidStateSigAfterFullStreamSettlesWithWinnerAndVotes(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	env := setupLiveFloorProxy(t, 3, nil, true)
	params := defaultParams()
	params.StartedAt = time.Now().Add(-10 * time.Second).Unix()
	prepared, err := env.session.PrepareInference(context.Background(), params)
	require.NoError(t, err)

	ctx := context.Background()
	rg := newRaceGroup(ctx, ctx, "escrow-proxy", httptest.NewRecorder())
	rg.setWinner(prepared.Nonce())

	now := time.Now()
	inf := &inflight{
		hostIdx:  prepared.HostIdx(),
		hostID:   env.session.HostLabel(prepared.HostIdx()),
		nonce:    prepared.Nonce(),
		escrowID: "escrow-proxy",
		sendTime: now.Add(-10 * time.Second),
		resp:     &host.HostResponse{Nonce: prepared.Nonce()},
		done:     make(chan struct{}),
	}
	inf.setReceiptAt(now.Add(-9 * time.Second))
	inf.setFirstTokenAt(now.Add(-8 * time.Second))
	inf.outputChunks.Store(3892)
	inf.contentChunks.Store(3892)
	// Simulate ProcessResponse failing with invalid state signature during a validator halt (#1387).
	inf.processOnce.Do(func() {
		inf.processErr = fmt.Errorf("%w: host 1 signer mismatch during halt", types.ErrInvalidStateSig)
	})
	time.AfterFunc(15*time.Millisecond, func() { close(inf.done) })

	err = env.proxy.redundancy.awaitRace(
		ctx,
		ctx,
		[]*inflight{inf},
		rg,
		params,
		Decision{Reason: "primary_unresponsive"},
		map[string]bool{},
		&cancelFlag{},
	)
	require.NoError(t, err, "full stream already delivered to client must not fail the request")

	require.Eventually(t, func() bool {
		return env.sm.SnapshotState().Inferences[prepared.Nonce()].Status == types.StatusTimedOut
	}, 10*time.Second, 25*time.Millisecond, "open nonce must still resolve via background timeout vote")
	env.proxy.redundancy.waitRaceCleanups()

	logText := logs.String()
	require.Contains(t, logText, "stage=winner_served_without_finish")
	require.NotContains(t, logText, "stage=winner_failed_after_content")
	require.Contains(t, logText, fmt.Sprintf("winner_nonce=%d", prepared.Nonce()))
	require.Contains(t, logText, "outcome=success")
}

func TestAwaitRace_WinnerStateHashMismatchStillFailsClosed(t *testing.T) {
	env := setupLiveFloorProxy(t, 3, nil, true)
	params := defaultParams()
	params.StartedAt = time.Now().Add(-10 * time.Second).Unix()
	prepared, err := env.session.PrepareInference(context.Background(), params)
	require.NoError(t, err)

	ctx := context.Background()
	rg := newRaceGroup(ctx, ctx, "escrow-proxy", httptest.NewRecorder())
	rg.setWinner(prepared.Nonce())

	now := time.Now()
	inf := &inflight{
		hostIdx:  prepared.HostIdx(),
		hostID:   env.session.HostLabel(prepared.HostIdx()),
		nonce:    prepared.Nonce(),
		escrowID: "escrow-proxy",
		sendTime: now.Add(-10 * time.Second),
		resp:     &host.HostResponse{Nonce: prepared.Nonce()},
		done:     make(chan struct{}),
	}
	inf.setReceiptAt(now.Add(-9 * time.Second))
	inf.setFirstTokenAt(now.Add(-8 * time.Second))
	inf.outputChunks.Store(10)
	inf.contentChunks.Store(10)
	inf.processOnce.Do(func() {
		inf.processErr = fmt.Errorf("apply diff: %w", types.ErrStateHashMismatch)
	})
	time.AfterFunc(15*time.Millisecond, func() { close(inf.done) })

	err = env.proxy.redundancy.awaitRace(
		ctx,
		ctx,
		[]*inflight{inf},
		rg,
		params,
		Decision{Reason: "primary_only"},
		map[string]bool{},
		&cancelFlag{},
	)
	require.ErrorIs(t, err, types.ErrStateHashMismatch)
	env.proxy.redundancy.waitRaceCleanups()
}
