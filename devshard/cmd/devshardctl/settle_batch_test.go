package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func blockingSettleFunc(entered chan<- string, release <-chan struct{}) settleEscrowFunc {
	return func(ctx context.Context, id string, req adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
		entered <- id
		<-release
		return &SettleDevshardEscrowResult{EscrowID: 1, TxHash: "hash-" + id, Settler: "settler"}, nil
	}
}

func waitForEntered(t *testing.T, entered <-chan string) string {
	t.Helper()
	select {
	case id := <-entered:
		return id
	case <-time.After(2 * time.Second):
		t.Fatal("settle never entered")
		return ""
	}
}

func TestRunSettleBatchSettlesWithinBatchSizeConcurrently(t *testing.T) {
	entered := make(chan string, 3)
	release := make(chan struct{})
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		runSettleBatch(t.Context(), rec, []string{"A", "B", "C"}, 2, adminSettleEscrowRequest{}, blockingSettleFunc(entered, release))
		close(done)
	}()

	first := waitForEntered(t, entered)
	second := waitForEntered(t, entered)
	require.ElementsMatch(t, []string{"A", "B"}, []string{first, second}, "batch_size=2 must run the first two escrows concurrently")

	select {
	case <-entered:
		t.Fatal("a third escrow must wait for a worker slot to free up")
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	third := waitForEntered(t, entered)
	require.Equal(t, "C", third)

	waitForDone(t, done, "runSettleBatch never returned")

	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	require.Len(t, lines, 3, "one NDJSON line per escrow")
}

func TestRunSettleBatchWritesResultPerEscrow(t *testing.T) {
	rec := httptest.NewRecorder()
	settle := func(ctx context.Context, id string, req adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
		if id == "bad" {
			return nil, errDevshardBusy
		}
		return &SettleDevshardEscrowResult{EscrowID: 9, TxHash: "hash", Settler: "settler"}, nil
	}

	runSettleBatch(t.Context(), rec, []string{"good", "bad"}, 2, adminSettleEscrowRequest{}, settle)

	results := map[string]adminSettleBatchResult{}
	for _, line := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n") {
		var result adminSettleBatchResult
		require.NoError(t, json.Unmarshal([]byte(line), &result))
		results[result.EscrowID] = result
	}
	require.True(t, results["good"].OK)
	require.Equal(t, "hash", results["good"].TxHash)
	require.False(t, results["bad"].OK)
	require.Equal(t, "busy", results["bad"].Reason)
}

func TestGatewayHandleAdminSettleBatchRequiresEscrowIDs(t *testing.T) {
	g := newFinalizeTestGateway(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/devshards/settle", strings.NewReader(`{"escrow_ids":[]}`))
	rec := httptest.NewRecorder()

	g.handleAdminDevshardAction(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRunSettleBatchKeepsSettlingAfterTheClientDisconnects(t *testing.T) {
	rec := httptest.NewRecorder()
	requestContext, disconnect := context.WithCancel(t.Context())
	disconnect()
	settle := func(ctx context.Context, id string, req adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			return nil, errors.New("settlement is not bounded")
		}
		return &SettleDevshardEscrowResult{EscrowID: 1, TxHash: "hash", Settler: "settler"}, nil
	}

	runSettleBatch(requestContext, rec, []string{"A"}, 1, adminSettleEscrowRequest{}, settle)

	var result adminSettleBatchResult
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(rec.Body.String())), &result))
	require.True(t, result.OK, "a settlement must not be cut off halfway because the admin client went away: %s", result.Error)
}

func TestDedupeEscrowIDsKeepsFirstOccurrenceOrder(t *testing.T) {
	require.Equal(t, []string{"A", "B", "C"}, dedupeEscrowIDs([]string{"A", "B", "A", "C", "B"}))
}

func TestRunSettleBatchRecoversFromPanicInOneEscrow(t *testing.T) {
	rec := httptest.NewRecorder()
	settle := func(ctx context.Context, id string, req adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
		if id == "boom" {
			panic("simulated settle panic")
		}
		return &SettleDevshardEscrowResult{EscrowID: 9, TxHash: "hash", Settler: "settler"}, nil
	}

	done := make(chan struct{})
	go func() {
		runSettleBatch(t.Context(), rec, []string{"boom", "fine"}, 2, adminSettleEscrowRequest{}, settle)
		close(done)
	}()
	waitForDone(t, done, "runSettleBatch must recover a panicking escrow, not crash the process")

	results := map[string]adminSettleBatchResult{}
	for _, line := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n") {
		var result adminSettleBatchResult
		require.NoError(t, json.Unmarshal([]byte(line), &result))
		results[result.EscrowID] = result
	}
	require.False(t, results["boom"].OK)
	require.Contains(t, results["boom"].Error, "simulated settle panic")
	require.True(t, results["fine"].OK, "one escrow panicking must not stop the others")
}

func TestGatewayHandleAdminSettleBatchRoutesToBatchHandler(t *testing.T) {
	rt := &devshardRuntime{id: "42", model: "m"}
	g := newFinalizeTestGateway(t, rt)
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/devshards/settle", strings.NewReader(`{"escrow_ids":["42"],"force":true}`))
	rec := httptest.NewRecorder()

	g.handleAdminDevshardAction(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/x-ndjson", rec.Header().Get("Content-Type"))
	var result adminSettleBatchResult
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(rec.Body.String())), &result))
	require.Equal(t, "42", result.EscrowID)
}
