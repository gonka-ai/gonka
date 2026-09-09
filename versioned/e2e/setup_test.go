//go:build e2e

package e2e

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

var (
	oracleURL            = envOrDefault("ORACLE_URL", "http://oracle:8080")
	versiondURL          = envOrDefault("VERSIOND_URL", "http://versiond:8080")
	versiondPollInterval = durationEnvOrDefault("VERSIOND_POLL_INTERVAL", 5*time.Second)
)

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func durationEnvOrDefault(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// buildTestappZip creates a zip archive containing the pre-built testapp binary
// at TESTAPP_PATH (protocol slot "testapp"). Returns the zip bytes and sha256 hash.
func buildTestappZip(t *testing.T) ([]byte, string) {
	t.Helper()
	return buildTestappZipFrom(t, envOrDefault("TESTAPP_PATH", "/app/build/testapp"))
}

// buildTestapp2Zip is the second protocol slot ("testapp2") for multi-version tests.
func buildTestapp2Zip(t *testing.T) ([]byte, string) {
	t.Helper()
	return buildTestappZipFrom(t, envOrDefault("TESTAPP2_PATH", "/app/build/testapp2"))
}

// buildTestappRolloutZip uses the same protocol slot as testapp but a different
// binary version and archive hash for same-name rolling update tests.
func buildTestappRolloutZip(t *testing.T) ([]byte, string) {
	t.Helper()
	return buildTestappZipFrom(t, envOrDefault("TESTAPP_ROLLOUT_PATH", "/app/build/testapp-rollout"))
}

func buildTestappZipFrom(t *testing.T, testappPath string) ([]byte, string) {
	t.Helper()

	binData, err := os.ReadFile(testappPath)
	if err != nil {
		t.Fatalf("read testapp binary %s: %v", testappPath, err)
	}

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("testapp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(binData); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	h := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(h[:])
}

// uploadBinary uploads the zip to the mock oracle's /binaries/ endpoint via PUT.
func uploadBinary(t *testing.T, name string, data []byte) {
	t.Helper()
	url := fmt.Sprintf("%s/binaries/%s", oracleURL, name)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload binary: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload binary status: %d", resp.StatusCode)
	}
}

// putVersion registers a version in the mock oracle.
func putVersion(t *testing.T, name, binaryURL, sha256Hash string, port int) {
	t.Helper()
	v := map[string]interface{}{
		"binary": binaryURL,
		"sha256": sha256Hash,
		"port":   port,
	}
	body, _ := json.Marshal(v)
	url := fmt.Sprintf("%s/versions/%s", oracleURL, name)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put version: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put version status: %d", resp.StatusCode)
	}
}

// deleteVersion removes a version from the mock oracle.
func deleteVersion(t *testing.T, name string) {
	t.Helper()
	url := fmt.Sprintf("%s/versions/%s", oracleURL, name)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete version: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete version status: %d", resp.StatusCode)
	}
}

// deleteAllVersions removes every version from the mock oracle.
func deleteAllVersions(t *testing.T) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, oracleURL+"/versions", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete all versions: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete all versions status: %d", resp.StatusCode)
	}
}

// setOracleFailure toggles the mock oracle's failure mode for version metadata.
func setOracleFailure(t *testing.T, enabled bool) {
	t.Helper()
	url := fmt.Sprintf("%s/fail?enabled=%t", oracleURL, enabled)
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("set oracle failure: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set oracle failure status: %d", resp.StatusCode)
	}
}

// assertOracleFailureMode verifies mock oracle GET /versions reflects failure mode.
func assertOracleFailureMode(t *testing.T, wantFailure bool) {
	t.Helper()
	resp, err := http.Get(oracleURL + "/versions")
	if err != nil {
		t.Fatalf("GET oracle versions: %v", err)
	}
	defer resp.Body.Close()

	if wantFailure {
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("oracle failure mode: status = %d, want 500", resp.StatusCode)
		}
		return
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("oracle healthy mode: status = %d, want 200", resp.StatusCode)
	}
}

// waitForVersion polls versiond until the given version responds through the proxy.
func waitForVersion(t *testing.T, version string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("%s/%s/", versiondURL, version)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("version %s not available after %v", version, timeout)
}

// waitForVersionUnavailable polls until the version stops returning 200.
func waitForVersionUnavailable(t *testing.T, version string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("%s/%s/", versiondURL, version)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err != nil {
			return
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return
		}
		resp.Body.Close()
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("version %s did not become temporarily unavailable after %v", version, timeout)
}

// waitForPollCycles waits long enough for versiond to observe oracle state changes.
func waitForPollCycles(cycles int) {
	time.Sleep(time.Duration(cycles)*versiondPollInterval + 2*time.Second)
}

// waitForVersionGone polls versiond until the given version is no longer proxied.
func waitForVersionGone(t *testing.T, version string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("%s/%s/", versiondURL, version)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil && resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("version %s still available after %v", version, timeout)
}

func waitForVersionPrefix(t *testing.T, version, wantPrefix string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("%s/%s/", versiondURL, version)
	for time.Now().Before(deadline) {
		var resp map[string]string
		if getJSONIfOK(url, &resp) == nil && resp["prefix"] == wantPrefix {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("version %s did not report prefix %q after %v", version, wantPrefix, timeout)
}

func waitForNoDraining(t *testing.T, version string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("%s/healthz", versiondURL)
	for time.Now().Before(deadline) {
		var statuses []map[string]interface{}
		if getJSONIfOK(url, &statuses) == nil {
			draining := false
			for _, s := range statuses {
				if s["name"] == version && s["status"] == "draining" {
					draining = true
					break
				}
			}
			if !draining {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("version %s still has draining child after %v", version, timeout)
}

// assertHealthStatus verifies that versiond reports the expected status for a version.
func assertHealthStatus(t *testing.T, version, wantStatus string) {
	t.Helper()
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

	for _, s := range statuses {
		if s["name"] == version {
			if s["status"] != wantStatus {
				t.Errorf("%s status = %q, want %s", version, s["status"], wantStatus)
			}
			return
		}
	}
	t.Fatalf("%s not found in healthz response: %s", version, string(body))
}

// getJSON does a GET and decodes the JSON response into out.
func getJSON(t *testing.T, url string, out interface{}) {
	t.Helper()
	if err := getJSONIfOK(url, out); err != nil {
		t.Fatal(err)
	}
}

func getJSONIfOK(url string, out interface{}) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET %s: status %d, body: %s", url, resp.StatusCode, string(body))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response from %s: %w", url, err)
	}
	return nil
}
