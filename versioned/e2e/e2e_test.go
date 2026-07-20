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

// Oracle slot names must match each binary's --print-protocol-version
// (default testapp embeds "testapp"; testapp2 embeds "testapp2").

func TestBasicFlow(t *testing.T) {
	zipData, hash := buildTestappZip(t)

	uploadBinary(t, "testapp.zip", zipData)

	binaryURL := fmt.Sprintf("%s/binaries/testapp.zip", oracleURL)
	putVersion(t, "testapp", binaryURL, hash, 9001)

	waitForVersion(t, "testapp", 90*time.Second)

	var resp map[string]string
	getJSON(t, fmt.Sprintf("%s/testapp/", versiondURL), &resp)
	if resp["prefix"] != "testapp" {
		t.Errorf("prefix = %q, want %q", resp["prefix"], "testapp")
	}
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

func TestRemoveVersion(t *testing.T) {
	zip1, hash1 := buildTestappZip(t)
	zip2, hash2 := buildTestapp2Zip(t)

	uploadBinary(t, "testapp.zip", zip1)
	uploadBinary(t, "testapp2.zip", zip2)
	putVersion(t, "testapp", fmt.Sprintf("%s/binaries/testapp.zip", oracleURL), hash1, 9001)
	putVersion(t, "testapp2", fmt.Sprintf("%s/binaries/testapp2.zip", oracleURL), hash2, 9002)
	waitForVersion(t, "testapp", 90*time.Second)
	waitForVersion(t, "testapp2", 90*time.Second)

	deleteVersion(t, "testapp")
	waitForVersionGone(t, "testapp", 90*time.Second)

	var resp map[string]string
	getJSON(t, fmt.Sprintf("%s/testapp2/", versiondURL), &resp)
	if resp["prefix"] != "testapp2" {
		t.Errorf("testapp2 prefix = %q", resp["prefix"])
	}
}

func TestHashMismatch(t *testing.T) {
	zipData, _ := buildTestappZip(t)

	// Upload binary but register with wrong hash under a third slot name.
	// Use testapp binary (protocol "testapp") with a mismatched slot so even a
	// correct hash would fail protocol checks; wrong hash fails earlier.
	uploadBinary(t, "bad.zip", zipData)
	putVersion(t, "badslot", fmt.Sprintf("%s/binaries/bad.zip", oracleURL), "wrong_hash", 9003)

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
