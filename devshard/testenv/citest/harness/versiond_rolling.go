package harness

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	cosrv "devshard/chainoracle/server"

	"github.com/stretchr/testify/require"
)

// VersiondHealthEntry mirrors versiond /healthz entries.
type VersiondHealthEntry struct {
	Name          string `json:"name"`
	Port          int    `json:"port"`
	Status        string `json:"status"`
	SHA256        string `json:"sha256,omitempty"`
	BinaryVersion string `json:"binary_version,omitempty"`
}

// WriteDevsharddZip writes a versiond-downloadable archive and returns its sha256.
func WriteDevsharddZip(t *testing.T, devsharddPath, zipPath, marker string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(zipPath), 0o755))

	src, err := os.Open(devsharddPath)
	require.NoError(t, err)
	defer src.Close()
	info, err := src.Stat()
	require.NoError(t, err)

	out, err := os.Create(zipPath)
	require.NoError(t, err)
	zw := zip.NewWriter(out)

	hdr, err := zip.FileInfoHeader(info)
	require.NoError(t, err)
	hdr.Name = "devshardd"
	hdr.Method = zip.Deflate
	hdr.SetMode(0o755)
	w, err := zw.CreateHeader(hdr)
	require.NoError(t, err)
	_, err = io.Copy(w, src)
	require.NoError(t, err)

	markerW, err := zw.Create("testenv-marker.txt")
	require.NoError(t, err)
	_, err = markerW.Write([]byte(marker))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.NoError(t, out.Close())

	archive, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	sum := sha256.Sum256(archive)
	return hex.EncodeToString(sum[:])
}

// PatchTestenvVersions replaces mock-dapi /versions for versiond polling.
func PatchTestenvVersions(t *testing.T, client *http.Client, mockDapiHTTP string, versions []cosrv.Version) {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	require.NoError(t, PostJSON(client, mockDapiHTTP+"/testenv/versions", cosrv.VersionConfig{
		Versions: versions,
	}, nil))
}

// VersiondHealth fetches a versiond container's local /healthz.
func VersiondHealth(t *testing.T, stack *Stack, service string) []VersiondHealthEntry {
	t.Helper()
	entries, err := TryVersiondHealth(stack, service)
	require.NoError(t, err)
	return entries
}

// TryVersiondHealth fetches a versiond container's local /healthz without failing the test.
func TryVersiondHealth(stack *Stack, service string) ([]VersiondHealthEntry, error) {
	out, err := stack.ComposeExecOutput(service, "wget", "-q", "-O", "-", "http://127.0.0.1:8080/healthz")
	if err != nil {
		return nil, err
	}
	var entries []VersiondHealthEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		return nil, fmt.Errorf("versiond health %s: %w: %s", service, err, out)
	}
	return entries, nil
}

// TryVersiondReady probes the readiness endpoint the router health-checks.
// It is on the traffic listener on purpose: a versiond that answers /readyz
// with 200 is exactly a versiond the router may send work to.
func TryVersiondReady(stack *Stack, service string) error {
	_, err := stack.ComposeExecOutput(
		service,
		"wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8080/readyz",
	)
	return err
}

// HasVersiondHealthEntry reports whether health contains the wanted version state.
func HasVersiondHealthEntry(entries []VersiondHealthEntry, name, status, sha string) bool {
	for _, e := range entries {
		if e.Name != name {
			continue
		}
		if status != "" && e.Status != status {
			continue
		}
		if sha != "" && e.SHA256 != sha {
			continue
		}
		return true
	}
	return false
}

// GatewayStreamResult is the result of a live gateway SSE request.
type GatewayStreamResult struct {
	Status  int
	Content string
	SawDone bool
	Err     error
	Body    string
}

// StartGatewayChatCompletionStream starts stream=true chat and closes accepted
// after HTTP 200 and the first content chunk.
func StartGatewayChatCompletionStream(
	client *http.Client,
	gatewayURL string,
	adminAPIKey string,
	req ChatCompletionRequest,
) (<-chan struct{}, <-chan GatewayStreamResult) {
	if client == nil {
		client = GatewayChatClient()
	}
	req.Stream = true
	accepted := make(chan struct{})
	result := make(chan GatewayStreamResult, 1)
	var acceptedOnce sync.Once
	closeAccepted := func() { acceptedOnce.Do(func() { close(accepted) }) }

	go func() {
		defer close(result)
		var res GatewayStreamResult
		data, err := json.Marshal(req)
		if err != nil {
			closeAccepted()
			res.Err = err
			result <- res
			return
		}
		httpReq, err := http.NewRequest(http.MethodPost, gatewayURL+"/v1/chat/completions", bytes.NewReader(data))
		if err != nil {
			closeAccepted()
			res.Err = err
			result <- res
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		if adminAPIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+adminAPIKey)
		}

		resp, err := client.Do(httpReq)
		if err != nil {
			closeAccepted()
			res.Err = err
			result <- res
			return
		}
		defer resp.Body.Close()
		res.Status = resp.StatusCode
		if resp.StatusCode >= 300 {
			closeAccepted()
			body, _ := io.ReadAll(resp.Body)
			res.Body = string(body)
			result <- res
			return
		}

		var assembled strings.Builder
		scanner := bufio.NewScanner(resp.Body)
		firstChunk := false
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "data: [DONE]" {
				res.SawDone = true
				continue
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimPrefix(line, "data: ")
			var chunk map[string]any
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				continue
			}
			content := streamChunkContent(chunk)
			if content == "" {
				continue
			}
			if !firstChunk {
				firstChunk = true
				closeAccepted()
			}
			assembled.WriteString(content)
		}
		if err := scanner.Err(); err != nil {
			res.Err = err
		}
		if !firstChunk {
			closeAccepted()
		}
		res.Content = assembled.String()
		result <- res
	}()

	return accepted, result
}

func streamChunkContent(chunk map[string]any) string {
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	choice, _ := choices[0].(map[string]any)
	delta, _ := choice["delta"].(map[string]any)
	if delta == nil {
		return ""
	}
	s, _ := delta["content"].(string)
	return s
}

// VersiondBinaryURL returns the mock-dapi URL a versiond container can download.
func VersiondBinaryURL(mockDapiPort int, fileName string) string {
	return fmt.Sprintf("http://mock-dapi:%d/testenv/binaries/%s", mockDapiPort, fileName)
}
