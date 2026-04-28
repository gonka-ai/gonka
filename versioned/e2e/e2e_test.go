//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestBasicFlow(t *testing.T) {
	zipData, hash := buildTestappZip(t)

	// Upload binary to oracle
	uploadBinary(t, "v1.zip", zipData)

	// Register version
	binaryURL := fmt.Sprintf("%s/binaries/v1.zip", oracleURL)
	putVersion(t, "v1", binaryURL, hash, 9001)

	// Wait for versiond to pick it up
	waitForVersion(t, "v1", 90*time.Second)

	// Verify response through proxy
	var resp map[string]string
	getJSON(t, fmt.Sprintf("%s/v1/", versiondURL), &resp)
	if resp["prefix"] != "v1" {
		t.Errorf("prefix = %q, want %q", resp["prefix"], "v1")
	}
}

func TestAddVersion(t *testing.T) {
	zipData, hash := buildTestappZip(t)

	// Set up v1
	uploadBinary(t, "v1.zip", zipData)
	binaryURL := fmt.Sprintf("%s/binaries/v1.zip", oracleURL)
	putVersion(t, "v1", binaryURL, hash, 9001)
	waitForVersion(t, "v1", 90*time.Second)

	// Add v2 (same binary, different port)
	uploadBinary(t, "v2.zip", zipData)
	binaryURL2 := fmt.Sprintf("%s/binaries/v2.zip", oracleURL)
	putVersion(t, "v2", binaryURL2, hash, 9002)
	waitForVersion(t, "v2", 90*time.Second)

	// Both should work
	var resp1, resp2 map[string]string
	getJSON(t, fmt.Sprintf("%s/v1/", versiondURL), &resp1)
	getJSON(t, fmt.Sprintf("%s/v2/", versiondURL), &resp2)
	if resp1["prefix"] != "v1" {
		t.Errorf("v1 prefix = %q", resp1["prefix"])
	}
	if resp2["prefix"] != "v2" {
		t.Errorf("v2 prefix = %q", resp2["prefix"])
	}
}

func TestRemoveVersion(t *testing.T) {
	zipData, hash := buildTestappZip(t)

	// Set up v1 and v2
	uploadBinary(t, "v1.zip", zipData)
	uploadBinary(t, "v2.zip", zipData)
	putVersion(t, "v1", fmt.Sprintf("%s/binaries/v1.zip", oracleURL), hash, 9001)
	putVersion(t, "v2", fmt.Sprintf("%s/binaries/v2.zip", oracleURL), hash, 9002)
	waitForVersion(t, "v1", 90*time.Second)
	waitForVersion(t, "v2", 90*time.Second)

	// Remove v1
	deleteVersion(t, "v1")
	waitForVersionGone(t, "v1", 90*time.Second)

	// v2 should still work
	var resp map[string]string
	getJSON(t, fmt.Sprintf("%s/v2/", versiondURL), &resp)
	if resp["prefix"] != "v2" {
		t.Errorf("v2 prefix = %q", resp["prefix"])
	}
}

func TestHashMismatch(t *testing.T) {
	zipData, _ := buildTestappZip(t)

	// Upload binary but register with wrong hash
	uploadBinary(t, "v3.zip", zipData)
	putVersion(t, "v3", fmt.Sprintf("%s/binaries/v3.zip", oracleURL), "wrong_hash", 9003)

	// Wait a couple poll cycles
	time.Sleep(10 * time.Second)

	// v3 should not be available
	resp, err := http.Get(fmt.Sprintf("%s/v3/", versiondURL))
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
	uploadBinary(t, "v1.zip", zipData)
	putVersion(t, "v1", fmt.Sprintf("%s/binaries/v1.zip", oracleURL), hash, 9001)
	waitForVersion(t, "v1", 90*time.Second)

	resp, err := http.Get(fmt.Sprintf("%s/v1/stream", versiondURL))
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
	uploadBinary(t, "v1.zip", zipData)
	putVersion(t, "v1", fmt.Sprintf("%s/binaries/v1.zip", oracleURL), hash, 9001)
	waitForVersion(t, "v1", 90*time.Second)

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
		if s["name"] == "v1" {
			found = true
			if s["status"] != "running" {
				t.Errorf("v1 status = %q, want running", s["status"])
			}
		}
	}
	if !found {
		t.Errorf("v1 not found in healthz response: %s", string(body))
	}
}
