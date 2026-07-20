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
	oracleURL   = envOrDefault("ORACLE_URL", "http://oracle:8080")
	versiondURL = envOrDefault("VERSIOND_URL", "http://versiond:8080")
)

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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

// getJSON does a GET and decodes the JSON response into out.
func getJSON(t *testing.T, url string, out interface{}) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status %d, body: %s", url, resp.StatusCode, string(body))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode response from %s: %v", url, err)
	}
}
