//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Oracle slot names must match each binary's --print-protocol-version
// (default testapp embeds "testapp"; testapp2 embeds "testapp2").

func TestBasicFlow(t *testing.T) {
	zipData, hash := buildTestappZip(t)

	uploadBinary(t, "testapp.zip", zipData)
	putVersion(t, "testapp", fmt.Sprintf("%s/binaries/testapp.zip", oracleURL), hash, 9001)
	waitForVersion(t, "testapp", 90*time.Second)

	var resp map[string]string
	getJSON(t, fmt.Sprintf("%s/testapp/", versiondURL), &resp)
	if resp["prefix"] != "testapp" {
		t.Errorf("prefix = %q, want %q", resp["prefix"], "testapp")
	}
}

func TestChildProcessCrashRecovery(t *testing.T) {
	zipData, hash := buildTestappZip(t)
	slot := "testapp"

	uploadBinary(t, "crash-testapp.zip", zipData)
	putVersion(t, slot, fmt.Sprintf("%s/binaries/crash-testapp.zip", oracleURL), hash, 9001)
	waitForVersion(t, slot, 90*time.Second)

	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/%s/exit", versiondURL, slot), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request child exit: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("child exit status = %d, want 204", resp.StatusCode)
	}

	waitForVersionUnavailable(t, slot, 25*time.Second)
	waitForVersion(t, slot, 90*time.Second)

	var recovered map[string]string
	getJSON(t, fmt.Sprintf("%s/%s/", versiondURL, slot), &recovered)
	if recovered["prefix"] != slot {
		t.Errorf("prefix = %q, want %q", recovered["prefix"], slot)
	}
}

func TestRegisterStartupVersion(t *testing.T) {
	if os.Getenv("REGISTER_STARTUP_VERSION") == "" {
		t.Skip("startup version registration is enabled only by the startup scenario")
	}

	zipData, hash := buildTestappZip(t)

	uploadBinary(t, "startup-testapp.zip", zipData)
	putVersion(t, "testapp", fmt.Sprintf("%s/binaries/startup-testapp.zip", oracleURL), hash, 9001)
}

func TestStartupFromExistingOracleState(t *testing.T) {
	if os.Getenv("EXPECT_INITIAL_TESTAPP") == "" {
		t.Skip("startup oracle state scenario is enabled only after versiond restart")
	}

	waitForVersion(t, "testapp", 90*time.Second)

	var proxied map[string]string
	getJSON(t, fmt.Sprintf("%s/testapp/", versiondURL), &proxied)
	if proxied["prefix"] != "testapp" {
		t.Errorf("prefix = %q, want %q", proxied["prefix"], "testapp")
	}
	assertHealthStatus(t, "testapp", "running")
}

func TestAddVersion(t *testing.T) {
	zip1, hash1 := buildTestappZip(t)
	zip2, hash2 := buildTestapp2Zip(t)

	uploadBinary(t, "testapp.zip", zip1)
	putVersion(t, "testapp", fmt.Sprintf("%s/binaries/testapp.zip", oracleURL), hash1, 9001)
	waitForVersion(t, "testapp", 90*time.Second)

	uploadBinary(t, "testapp2.zip", zip2)
	putVersion(t, "testapp2", fmt.Sprintf("%s/binaries/testapp2.zip", oracleURL), hash2, 9002)
	waitForVersion(t, "testapp2", 90*time.Second)

	var resp1, resp2 map[string]string
	getJSON(t, fmt.Sprintf("%s/testapp/", versiondURL), &resp1)
	getJSON(t, fmt.Sprintf("%s/testapp2/", versiondURL), &resp2)
	if resp1["prefix"] != "testapp" {
		t.Errorf("testapp prefix = %q", resp1["prefix"])
	}
	if resp2["prefix"] != "testapp2" {
		t.Errorf("testapp2 prefix = %q", resp2["prefix"])
	}
}

func TestCatalogRemovalKeepsAcceptedVersionRunning(t *testing.T) {
	zip1, hash1 := buildTestappZip(t)
	zip2, hash2 := buildTestapp2Zip(t)

	uploadBinary(t, "testapp.zip", zip1)
	uploadBinary(t, "testapp2.zip", zip2)
	putVersion(t, "testapp", fmt.Sprintf("%s/binaries/testapp.zip", oracleURL), hash1, 9001)
	putVersion(t, "testapp2", fmt.Sprintf("%s/binaries/testapp2.zip", oracleURL), hash2, 9002)
	waitForVersion(t, "testapp", 90*time.Second)
	waitForVersion(t, "testapp2", 90*time.Second)

	deleteVersion(t, "testapp")
	waitForPollCycles(3)

	for _, version := range []string{"testapp", "testapp2"} {
		var resp map[string]string
		getJSON(t, fmt.Sprintf("%s/%s/", versiondURL, version), &resp)
		if resp["prefix"] != version {
			t.Errorf("%s prefix = %q", version, resp["prefix"])
		}
	}
}

func TestOracleTemporaryFailureKeepsVersionsRunning(t *testing.T) {
	setOracleFailure(t, false)
	t.Cleanup(func() {
		setOracleFailure(t, false)
	})

	zipData, hash := buildTestappZip(t)
	slot := "testapp"

	uploadBinary(t, "oracle-failure-testapp.zip", zipData)
	putVersion(t, slot, fmt.Sprintf("%s/binaries/oracle-failure-testapp.zip", oracleURL), hash, 9004)
	waitForVersion(t, slot, 90*time.Second)

	setOracleFailure(t, true)
	assertOracleFailureMode(t, true)
	waitForPollCycles(3)

	var resp map[string]string
	getJSON(t, fmt.Sprintf("%s/%s/", versiondURL, slot), &resp)
	if resp["prefix"] != slot {
		t.Errorf("prefix = %q, want %q", resp["prefix"], slot)
	}
}

func TestEmptyOracleResponseKeepsVersionsRunning(t *testing.T) {
	setOracleFailure(t, false)
	deleteAllVersions(t)

	zipData, hash := buildTestappZip(t)

	uploadBinary(t, "empty-oracle-testapp.zip", zipData)
	putVersion(t, "testapp", fmt.Sprintf("%s/binaries/empty-oracle-testapp.zip", oracleURL), hash, 9001)
	waitForVersion(t, "testapp", 90*time.Second)

	deleteVersion(t, "testapp")
	waitForPollCycles(3)

	var resp map[string]string
	getJSON(t, fmt.Sprintf("%s/testapp/", versiondURL), &resp)
	if resp["prefix"] != "testapp" {
		t.Errorf("prefix = %q, want %q", resp["prefix"], "testapp")
	}
}

func TestFailedSameVersionUpdateKeepsOldChildRunning(t *testing.T) {
	setOracleFailure(t, false)

	zipData, hash := buildTestappZip(t)

	uploadBinary(t, "failed-update-testapp.zip", zipData)
	putVersion(t, "testapp", fmt.Sprintf("%s/binaries/failed-update-testapp.zip", oracleURL), hash, 9001)
	waitForVersion(t, "testapp", 90*time.Second)

	uploadBinary(t, "failed-update-testapp-bad.zip", zipData)
	putVersion(t, "testapp", fmt.Sprintf("%s/binaries/failed-update-testapp-bad.zip", oracleURL), "wrong_hash", 9001)

	waitForPollCycles(3)

	var resp map[string]string
	getJSON(t, fmt.Sprintf("%s/testapp/", versiondURL), &resp)
	if resp["prefix"] != "testapp" {
		t.Errorf("prefix = %q, want %q", resp["prefix"], "testapp")
	}
	assertHealthStatus(t, "testapp", "running")
}

func TestHashMismatch(t *testing.T) {
	zipData, _ := buildTestappZip(t)

	// Upload binary but register with wrong hash under a third slot name.
	// Use testapp binary (protocol "testapp") with a mismatched slot so even a
	// correct hash would fail protocol checks; wrong hash fails earlier.
	uploadBinary(t, "bad.zip", zipData)
	putVersion(t, "badslot", fmt.Sprintf("%s/binaries/bad.zip", oracleURL), strings.Repeat("0", 64), 9003)

	time.Sleep(10 * time.Second)

	resp, err := http.Get(fmt.Sprintf("%s/badslot/", versiondURL))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestSSEStreaming(t *testing.T) {
	zipData, hash := buildTestappZip(t)
	uploadBinary(t, "testapp.zip", zipData)
	putVersion(t, "testapp", fmt.Sprintf("%s/binaries/testapp.zip", oracleURL), hash, 9001)
	waitForVersion(t, "testapp", 90*time.Second)

	resp, err := http.Get(fmt.Sprintf("%s/testapp/stream", versiondURL))
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()

	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Errorf("Content-Type = %q", resp.Header.Get("Content-Type"))
	}

	scanner := bufio.NewScanner(resp.Body)
	var events []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			events = append(events, line)
		}
	}
	if len(events) != 5 {
		t.Errorf("got %d events, want 5", len(events))
	}
}

func TestHealthEndpoint(t *testing.T) {
	zipData, hash := buildTestappZip(t)
	uploadBinary(t, "testapp.zip", zipData)
	putVersion(t, "testapp", fmt.Sprintf("%s/binaries/testapp.zip", oracleURL), hash, 9001)
	waitForVersion(t, "testapp", 90*time.Second)

	resp, err := http.Get(fmt.Sprintf("%s/healthz", versiondURL))
	if err != nil {
		t.Fatalf("GET healthz: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var statuses []map[string]interface{}
	if err := json.Unmarshal(body, &statuses); err != nil {
		t.Fatalf("decode healthz: %v, body: %s", err, string(body))
	}

	found := false
	for _, s := range statuses {
		if s["name"] == "testapp" {
			found = true
			if s["status"] != "running" {
				t.Errorf("testapp status = %q, want running", s["status"])
			}
		}
	}
	if !found {
		t.Errorf("testapp not found in healthz response: %s", string(body))
	}
}

func TestSameNameNewSHA_RollingUpdateDrainsOld(t *testing.T) {
	oldZip, oldHash := buildTestappZip(t)
	newZip, newHash := buildTestappRolloutZip(t)

	uploadBinary(t, "testapp-old.zip", oldZip)
	putVersion(t, "testapp", fmt.Sprintf("%s/binaries/testapp-old.zip", oracleURL), oldHash, 9001)
	waitForVersionPrefix(t, "testapp", "testapp", 90*time.Second)

	type result struct {
		resp map[string]string
		err  error
	}
	slowDone := make(chan result, 1)
	go func() {
		var resp map[string]string
		err := getJSONIfOK(fmt.Sprintf("%s/testapp/slow?duration=8s", versiondURL), &resp)
		slowDone <- result{resp: resp, err: err}
	}()

	time.Sleep(500 * time.Millisecond)

	uploadBinary(t, "testapp-new.zip", newZip)
	putVersion(t, "testapp", fmt.Sprintf("%s/binaries/testapp-new.zip", oracleURL), newHash, 9001)
	waitForVersionPrefix(t, "testapp", "testapp-rollout", 90*time.Second)

	select {
	case got := <-slowDone:
		if got.err != nil {
			t.Fatalf("slow request failed: %v", got.err)
		}
		if got.resp["prefix"] != "testapp" {
			t.Fatalf("slow request prefix = %q, want old prefix %q", got.resp["prefix"], "testapp")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("slow request did not finish")
	}

	waitForNoDraining(t, "testapp", 30*time.Second)
	var resp map[string]string
	getJSON(t, fmt.Sprintf("%s/testapp/", versiondURL), &resp)
	if resp["prefix"] != "testapp-rollout" {
		t.Fatalf("new request prefix = %q, want %q", resp["prefix"], "testapp-rollout")
	}
}
