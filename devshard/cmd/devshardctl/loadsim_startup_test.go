//go:build loadsim

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const startupSimulationBatch = 5000

// startupSimulationSettings are the knobs of one startup run, read from STARTUPSIM_* variables. See devshard/docs/gateway-load-simulation.md.
type startupSimulationSettings struct {
	requests          int
	samplesPerRequest int
	days              int
	directory         string
	prune             bool
}

func startupSimulationSettingsFromEnv(t *testing.T) startupSimulationSettings {
	t.Helper()
	settings := startupSimulationSettings{
		requests:          readPositiveIntEnv(t, "STARTUPSIM_REQUESTS", 200_000),
		samplesPerRequest: readPositiveIntEnv(t, "STARTUPSIM_SAMPLES_PER_REQUEST", 3),
		days:              readPositiveIntEnv(t, "STARTUPSIM_DAYS", 120),
		directory:         os.Getenv("STARTUPSIM_DIR"),
		prune:             os.Getenv("STARTUPSIM_PRUNE") == "1",
	}
	if settings.directory == "" {
		settings.directory = t.TempDir()
	}
	return settings
}

// fillPerfStoreLikeProduction writes each simulated request's samples, request log row and accounting rows in turn, the way serving interleaves them.
func fillPerfStoreLikeProduction(t *testing.T, store *PerfStore, settings startupSimulationSettings) {
	t.Helper()
	end := time.Now()
	start := end.Add(-time.Duration(settings.days) * 24 * time.Hour)
	step := end.Sub(start) / time.Duration(settings.requests)
	hosts := make([]HostInvolvement, settings.samplesPerRequest)
	for request := range settings.requests {
		if request%startupSimulationBatch == 0 {
			if request > 0 {
				_, err := store.db.Exec("COMMIT")
				require.NoError(t, err)
			}
			_, err := store.db.Exec("BEGIN")
			require.NoError(t, err)
		}
		sentAt := start.Add(time.Duration(request) * step)
		requestID := fmt.Sprintf("chatcmpl-%016x", request)
		escrowID := strconv.Itoa(90_000 + request%400)
		for slot := range settings.samplesPerRequest {
			participant := fmt.Sprintf("gonka1%038d", (request*7+slot)%300)
			require.NoError(t, store.InsertSample(RequestSample{
				HostIdx: slot, ParticipantKey: participant, Responsive: true, SendTime: sentAt,
				ReceiptTime: sentAt.Add(300 * time.Millisecond), FirstToken: sentAt.Add(900 * time.Millisecond),
				TotalTime: 4 * time.Second, InputTokens: 5000,
			}))
			hosts[slot] = HostInvolvement{
				HostIdx: slot, ParticipantKey: participant, Nonce: uint64(request*settings.samplesPerRequest + slot + 1),
				OutputChunks: 120, ReceiptTimeMs: 300, FirstTokenMs: 900, TotalTimeMs: 4000, Responsive: true, Finished: true, Winner: slot == 0,
			}
			require.NoError(t, store.UpsertAccountingAttempt(RequestAccountingAttempt{
				RequestID: requestID, EscrowID: escrowID, Nonce: hosts[slot].Nonce, HostIdx: slot,
				ParticipantKey: participant, Winner: slot == 0, CreatedAt: sentAt,
			}))
		}
		require.NoError(t, store.InsertRequest(RequestRecord{
			Timestamp: sentAt, Model: "deepseek-ai/DeepSeek-V4-Flash-0731", InputTokens: 5000,
			WinnerNonce: hosts[0].Nonce, Decision: "speculative", Hosts: hosts,
		}))
		require.NoError(t, store.UpsertAccountingRequest(requestID, escrowID, "deepseek-ai/DeepSeek-V4-Flash-0731", sentAt))
		_, err := store.db.Exec(`UPDATE request_accounting SET completed_at = ?, outcome = 'success', decision = 'speculative', winner_nonce = ? WHERE request_id = ? AND escrow_id = ?`,
			sentAt.Add(4*time.Second).Format(time.RFC3339Nano), hosts[0].Nonce, requestID, escrowID)
		require.NoError(t, err)
	}
	_, err := store.db.Exec("COMMIT")
	require.NoError(t, err)
}

// Test flow:
//  1. Unless STARTUPSIM_DIR already holds a perf.db, fill one with STARTUPSIM_REQUESTS requests spread over STARTUPSIM_DAYS, interleaving every table the way serving does.
//  2. Open it the way mustBuildGateway does: NewPerfStore, then NewPerfTracker, timing each.
//  3. Report the file size, the row counts and both timings.
//  4. With STARTUPSIM_PRUNE=1, run one prune pass at production pacing while a writer inserts a sample every 10ms, and report what it deleted, how long it took and the writer's latency.
func TestStartupSimulation(t *testing.T) {
	settings := startupSimulationSettingsFromEnv(t)
	path := filepath.Join(settings.directory, "perf.db")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		filling, err := NewPerfStore(path)
		require.NoError(t, err)
		started := time.Now()
		fillPerfStoreLikeProduction(t, filling, settings)
		_, err = filling.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		require.NoError(t, err)
		require.NoError(t, filling.Close())
		t.Logf("filled %s with %d requests in %s", path, settings.requests, time.Since(started).Round(time.Second))
	}
	info, err := os.Stat(path)
	require.NoError(t, err)

	openStarted := time.Now()
	store, err := NewPerfStore(path)
	require.NoError(t, err)
	openElapsed := time.Since(openStarted)
	t.Cleanup(func() { _ = store.Close() })
	trackerStarted := time.Now()
	NewPerfTracker(store)
	trackerElapsed := time.Since(trackerStarted)

	var samples, requests int
	require.NoError(t, store.db.QueryRow("SELECT count(*) FROM perf_host_samples").Scan(&samples))
	require.NoError(t, store.db.QueryRow("SELECT count(*) FROM perf_request_log").Scan(&requests))
	t.Logf("perf.db %.1f MB, %d host samples, %d request log rows", float64(info.Size())/1e6, samples, requests)
	t.Logf("NewPerfStore %s, NewPerfTracker %s", openElapsed.Round(time.Millisecond), trackerElapsed.Round(time.Millisecond))
	if settings.prune {
		measurePruneUnderWrites(t, store)
	}
}

// measurePruneUnderWrites runs one production-paced prune pass beside a request-path writer and logs both sides.
func measurePruneUnderWrites(t *testing.T, store *PerfStore) {
	t.Helper()
	pruner := newPerfPruner(store, func(string) bool { return false }, func() uint64 { return 1 }, perfPrunerTiming{pause: perfPrunePause})
	stopWriting := make(chan struct{})
	var latencies []time.Duration
	var writer sync.WaitGroup
	writer.Go(func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopWriting:
				return
			case <-ticker.C:
			}
			started := time.Now()
			if err := store.InsertSample(RequestSample{ParticipantKey: "writer", SendTime: started, Responsive: true}); err != nil {
				t.Errorf("InsertSample() = %v, want nil", err)
				return
			}
			latencies = append(latencies, time.Since(started))
		}
	})
	started := time.Now()
	result := pruner.runOnce()
	elapsed := time.Since(started)
	close(stopWriting)
	writer.Wait()
	slices.Sort(latencies)
	t.Logf("prune pass %s: %d samples, %d request log rows, %d accounting rows deleted", elapsed.Round(time.Millisecond), result.samples, result.requestLog, result.accounting)
	t.Logf("writer during prune: %d inserts, p50 %s, p99 %s, max %s", len(latencies), percentileDuration(latencies, 0.5), percentileDuration(latencies, 0.99), percentileDuration(latencies, 1))
	reloadStarted := time.Now()
	NewPerfTracker(store)
	t.Logf("NewPerfTracker after prune %s", time.Since(reloadStarted).Round(time.Millisecond))
}
