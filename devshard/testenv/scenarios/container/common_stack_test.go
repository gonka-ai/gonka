//go:build testenvci

package container

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// startHeightSyncContainerStack brings up (or attaches to) the testenv compose stack and waits
// until height-sync, devshardctl, Loki, and VictoriaMetrics are ready.
//
// Isolated mode (default for bare go test): temp workspace + unique compose project per test.
// Reuse mode (run-container-heightsync-e2e.sh): TESTENV_REUSE_STACK=1, shared project in
// devshard/testenv. Tests advance the session to the next sync-turn lead (or other
// relative nonce) via advanceSessionToNonce — no per-test DB reset. Opt-in reset:
// TESTENV_RESET_STACK_DB=1 or driver RESET_SESSION=1.
func startHeightSyncContainerStack(t *testing.T) (ws, project string, httpClient, streamClient *http.Client, t0 time.Time) {
	t.Helper()
	if os.Getenv("TESTENV_SKIP_DOCKER_STACK") == "1" {
		t.Skip("TESTENV_SKIP_DOCKER_STACK=1")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}

	deadline, ok := t.Deadline()
	if !ok {
		deadline = time.Now().Add(20 * time.Minute)
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)

	httpClient = &http.Client{Timeout: 15 * time.Second}
	streamClient = &http.Client{Timeout: 5 * time.Minute}

	if ReuseContainerE2EStack() {
		PrintReuseStack(t)
		ws = TestenvDir(t)
		project = ContainerE2EComposeProject()
		composeFile := filepath.Join(ws, "docker-compose.yml")
		if _, err := os.Stat(composeFile); err != nil {
			t.Fatalf("testenv docker-compose.yml: %v", err)
		}
		if os.Getenv("TESTENV_RESET_STACK_DB") == "1" {
			resetSharedStackHostDB(t, ws, project)
		}
		t.Cleanup(cancel)
		waitHeightSyncStackReady(t, ctx, httpClient)
		t0 = time.Now().Add(-30 * time.Second)
		return ws, project, httpClient, streamClient, t0
	}

	// Isolated stack: copy compose into t.TempDir(), unique project, teardown on cleanup.
	ws = PrepareIsolatedE2EWorkspace(t)
	composeFile := filepath.Join(ws, "docker-compose.yml")
	if _, err := os.Stat(composeFile); err != nil {
		t.Fatalf("workspace docker-compose.yml: %v", err)
	}

	project = ComposeProjectForTest(t)

	down := func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer dcancel()
		_ = DockerCompose(dctx, ws, project, nil, nil, "down", "--remove-orphans", "--timeout", "60").Run()
	}
	down()
	t.Cleanup(func() {
		cancel()
		down()
	})

	PruneStaleContainerE2EDockerStacks(t)

	up := DockerCompose(ctx, ws, project, os.Stdout, os.Stderr, "up", "-d", "--build")
	if err := up.Run(); err != nil {
		t.Fatalf("docker compose up: %v", err)
	}

	LogComposeDebugHints(t, ws, project)
	WaitCoreStackServicesRunningOrFail(t, ctx, ws, project, time.Now().Add(4*time.Minute))
	waitHeightSyncStackReady(t, ctx, httpClient)

	t0 = time.Now().Add(-30 * time.Second)
	return ws, project, httpClient, streamClient, t0
}

func waitHeightSyncStackReady(t *testing.T, ctx context.Context, httpClient *http.Client) {
	t.Helper()
	WaitHeightSyncPositive(t, httpClient, time.Now().Add(5*time.Minute))
	WaitHTTP_OK(t, httpClient, "http://127.0.0.1:8081/v1/status", time.Now().Add(4*time.Minute), "devshardctl /v1/status")
	WaitHTTP_OK(t, httpClient, "http://127.0.0.1:3100/ready", time.Now().Add(3*time.Minute), "loki")
	WaitHTTP_OK(t, httpClient, "http://127.0.0.1:8428/api/v1/query?query=1", time.Now().Add(3*time.Minute), "victoria-metrics")
	WaitMockdapiBlockOracleConsumersReady(t, httpClient)
	assertHostsEscrowAlignedWithCourier(t, httpClient)
}
