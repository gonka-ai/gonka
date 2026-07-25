package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// HTTPClient is the default client for stack health polling.
func HTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// WaitGETOK polls url until GET returns HTTP 200, logging progress every few attempts.
// Pass an optional Stack to dump compose logs on timeout.
func WaitGETOK(t *testing.T, client *http.Client, url string, timeout time.Duration, label string, stack ...*Stack) {
	t.Helper()
	t.Logf("citest: waiting for %s → %s (timeout %s)", label, url, timeout)

	var attempts int
	var lastErr string
	ok := assertEventually(t, timeout, 2*time.Second, func() bool {
		attempts++
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err.Error()
			maybeLogWaitAttempt(t, label, attempts, lastErr)
			return false
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			snippet := strings.TrimSpace(string(body))
			if snippet == "" {
				lastErr = fmt.Sprintf("HTTP %d", resp.StatusCode)
			} else {
				lastErr = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, snippet)
			}
			maybeLogWaitAttempt(t, label, attempts, lastErr)
			return false
		}
		return true
	})
	if !ok {
		if len(stack) > 0 && stack[0] != nil {
			DumpComposeLogs(t, stack[0], "versiond-0", "versiond-1", "versiond-router", "devshardctl", "mock-dapi")
		}
		msg := fmt.Sprintf("citest: %s not ready after %d attempts (%s): %s", label, attempts, url, lastErr)
		if strings.Contains(url, "/v1/epochs/latest") && strings.Contains(lastErr, "404") {
			msg += " (stale devshard-mock-dapi image? run: make -C devshard/testenv dev-build or docker compose build mock-dapi)"
		}
		t.Fatal(msg)
	}
	if attempts > 1 {
		t.Logf("citest: %s ready after %d attempts", label, attempts)
	}
}

func maybeLogWaitAttempt(t *testing.T, label string, attempts int, detail string) {
	t.Helper()
	if attempts == 1 || attempts%5 == 0 {
		t.Logf("citest: %s attempt %d: %s", label, attempts, detail)
	}
}

// AssertEventually polls fn until it returns true or wait elapses.
func AssertEventually(t *testing.T, wait time.Duration, tick time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(tick)
	}
	return fn()
}

// assertEventually is a thin wrapper so we can fail with the last probe error.
func assertEventually(t *testing.T, wait time.Duration, tick time.Duration, fn func() bool) bool {
	t.Helper()
	return AssertEventually(t, wait, tick, fn)
}

// GetJSON performs GET and unmarshals JSON on success.
func GetJSON(client *http.Client, url string, dest any) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: %d %s", url, resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, dest)
}

// PostJSON performs POST with a JSON body and unmarshals the response.
func PostJSON(client *http.Client, url string, payload any, dest any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s: %d %s", url, resp.StatusCode, string(body))
	}
	if dest == nil {
		return nil
	}
	return json.Unmarshal(body, dest)
}
