//go:build testenvci

package citest

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devshard/host"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

func TestV4SyncRecoveryUsesSnapshotBeforeServing(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack := harness.NewStack(t, "citest-v4-snapshot-recovery-*")
	harness.RequireLinuxDevshardd(t, stack.TestenvDir)
	harness.WriteMultiConfig(t, stack.WorkDir, harness.MultiConfigOpts{
		Hosts:        2,
		EscrowSlots:  2,
		EscrowAmount: 1_000_000_000,
	})
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	require.Len(t, cfg.Hosts, 2)

	targetHost := cfg.Hosts[0].ID
	harness.PatchVersiondStorageMode(t, stack.ComposePath, "sqlite")
	harness.PatchRouterVersiondHosts(t, stack.ComposePath, targetHost)
	stack.Up(t)
	stack.StopService(t, cfg.Hosts[1].ID)

	eps := stack.Endpoints(t, cfg)
	client := harness.HTTPClient()
	chatClient := harness.GatewayChatClient()
	adminKey := harness.TestenvAdminAPIKey
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", targetHost, "versiond-router")
		}
	})

	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, chatClient, eps.GatewayHTTP, 3*time.Minute, stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+cfg.Versiond.VersionName+"/healthz",
		5*time.Minute, "initial devshardd health via router", stack)

	model := config.PrimaryModelID(cfg)
	for requestNumber := 1; requestNumber <= 4; requestNumber++ {
		harness.PostGatewayChatCompletion(t, chatClient, eps.GatewayHTTP, adminKey,
			harness.ChatCompletionRequest{
				Model: model,
				Messages: []harness.ChatMessage{{
					Role:    "user",
					Content: fmt.Sprintf("citest snapshot recovery request %d", requestNumber),
				}},
				MaxTokens: 16,
			})
	}
	snapshot := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	require.Greater(t, snapshot.LatestNonce, uint64(0))

	sessionURL := harness.RouterSessionURL(
		eps.RouterHTTP,
		cfg.Versiond.VersionName,
		snapshot.EscrowID,
		"/mempool",
	)
	outageStarted := time.Now()
	probeCtx, cancelProbe := context.WithCancel(context.Background())
	probeDone := make(chan map[int]int, 1)
	go probeStatuses(probeCtx, client, sessionURL, probeDone)

	harness.Step(t, "restart %s with offline snapshot tail", targetHost)
	stack.StopService(t, targetHost)
	snapshotNonce, latestNonce := prepareOfflineHostSnapshotTail(
		t,
		filepath.Join(stack.WorkDir, "data", targetHost, cfg.Versiond.VersionName),
		snapshot.EscrowID,
	)
	recoveryStarted := time.Now()
	stack.StartService(t, targetHost)
	harness.WaitVersiondSessionHealthy(t, stack, cfg, eps, snapshot.EscrowID)
	recoveryDuration := time.Since(recoveryStarted)
	cancelProbe()
	statusCounts := <-probeDone

	after := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	harness.RequireGatewaySessionStable(t, snapshot, after)
	harness.PostGatewayChatCompletion(t, chatClient, eps.GatewayHTTP, adminKey,
		harness.ChatCompletionRequest{
			Model: model,
			Messages: []harness.ChatMessage{{
				Role:    "user",
				Content: "citest snapshot recovery after restart",
			}},
			MaxTokens: 16,
		})
	advanced := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	harness.RequireGatewaySessionAdvanced(t, after, advanced)

	logs, err := stack.ComposeLogsSince(outageStarted.Add(-time.Second), targetHost)
	require.NoError(t, err)
	require.Contains(t, logs, "restored devshard snapshot")
	require.Contains(t, logs, "escrow_id="+snapshot.EscrowID)
	require.Contains(t, logs, fmt.Sprintf("snapshot_nonce=%d", snapshotNonce))
	require.Contains(t, logs, fmt.Sprintf("replayed_diffs=%d", latestNonce-snapshotNonce))
	require.LessOrEqual(t, statusCounts[http.StatusBadGateway], 1,
		"snapshot recovery must not expose a sustained HTTP 502 sequence")
	require.Zero(t, statusCounts[0], "snapshot recovery must not drop transport connections")
	require.Equal(t, 1, strings.Count(logs, "starting child version="+cfg.Versiond.VersionName),
		"snapshot recovery must not enter a child restart loop")

	harness.Step(t, "snapshot restart completed duration=%s status_counts=%v", recoveryDuration, statusCounts)
}

func prepareOfflineHostSnapshotTail(t *testing.T, dataDir, escrowID string) (uint64, uint64) {
	t.Helper()

	t.Setenv("DEVSHARD_STORAGE_MODE", "sqlite")
	store, err := storage.NewStorage(context.Background(), dataDir)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	meta, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, meta.LatestNonce, uint64(3))
	snapshotNonce := meta.LatestNonce - 2
	machine, err := state.NewStateMachine(
		escrowID,
		meta.Config,
		meta.Group,
		meta.InitialBalance,
		meta.CreatorAddr,
		signing.NewSecp256k1Verifier(),
		store,
		state.WithVersion(meta.Version),
	)
	require.NoError(t, err)
	records, err := store.GetDiffs(escrowID, 1, meta.LatestNonce)
	require.NoError(t, err)
	for _, record := range records {
		machine.InjectWarmKeys(record.WarmKeyDelta)
		_, err := machine.ApplyLocalPersisted(record.Nonce, record.Txs)
		require.NoError(t, err)
		if record.Nonce != snapshotNonce {
			continue
		}
		snapshot, err := host.MarshalStateSnapshotWithCommitted(
			machine.ExportState(),
			machine.ExportCommittedEntries(),
			machine.ExportSealedNonces(),
		)
		require.NoError(t, err)
		require.NoError(t, store.SaveSnapshot(escrowID, record.Nonce, snapshot))
	}
	return snapshotNonce, meta.LatestNonce
}

func probeStatuses(ctx context.Context, client *http.Client, url string, done chan<- map[int]int) {
	counts := make(map[int]int)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	defer func() { done <- counts }()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				counts[0]++
				continue
			}
			resp, err := client.Do(req)
			if err != nil {
				counts[0]++
				continue
			}
			_ = resp.Body.Close()
			counts[resp.StatusCode]++
		}
	}
}
