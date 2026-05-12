package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"time"
)

type proxyHandle struct {
	escrowID uint64
	proxyURL string
	cmd      *exec.Cmd
	done     chan struct{}
}

// Test inferences are intentionally small, so a short timeout keeps failed test
// runs from hanging without cutting off expected responses.
var proxyHTTPClient = &http.Client{Timeout: 5 * time.Second}

type settlement struct {
	EscrowID   string     `json:"escrow_id"`
	Nonce      uint64     `json:"nonce"`
	HostStats  []hostStat `json:"host_stats"`
	Signatures []slotSig  `json:"signatures"`
}

type hostStat struct {
	SlotID uint32 `json:"slot_id"`
	Cost   uint64 `json:"cost"`
}

type slotSig struct {
	SlotID    uint32 `json:"slot_id"`
	Signature string `json:"signature"`
}

func startProxy(bin string, escrowID uint64, privKey, rest, routePrefix string, port int) (*proxyHandle, error) {
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"DEVSHARD_PRIVATE_KEY="+privKey,
		fmt.Sprintf("DEVSHARD_ESCROW_ID=%d", escrowID),
		"DEVSHARD_CHAIN_REST="+rest,
		fmt.Sprintf("DEVSHARD_PORT=%d", port),
		"DEVSHARD_ROUTE_PREFIX="+routePrefix,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start devshardctl: %w", err)
	}
	h := &proxyHandle{
		escrowID: escrowID,
		proxyURL: fmt.Sprintf("http://localhost:%d", port),
		cmd:      cmd,
		done:     make(chan struct{}),
	}
	go func() { _ = h.cmd.Wait(); close(h.done) }()
	if err := h.waitReady(30 * time.Second); err != nil {
		h.stop()
		return nil, fmt.Errorf("devshardctl not ready: %w", err)
	}
	return h, nil
}

func (h *proxyHandle) stop() {
	if h.cmd != nil && h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
		<-h.done
	}
}

func (h *proxyHandle) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-h.done:
			return fmt.Errorf("process exited before becoming ready")
		default:
		}
		resp, err := proxyHTTPClient.Get(h.proxyURL + "/v1/status")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("not ready after %s", timeout)
}

func queryNonce(proxyURL string) (uint64, error) {
	resp, err := proxyHTTPClient.Get(proxyURL + "/v1/status")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("GET /v1/status returned status %d: %s", resp.StatusCode, string(body))
	}
	var s struct {
		Nonce uint64 `json:"nonce"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return 0, fmt.Errorf("decode /v1/status: %w", err)
	}
	return s.Nonce, nil
}

func waitNonceAdvanced(proxyURL string, current uint64, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if n, err := queryNonce(proxyURL); err == nil && n > current {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func sendInference(proxyURL, model string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "Write a friendly three-word greeting, then explain it in one short sentence."},
		},
		"max_tokens": 20,
	})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	resp, err := proxyHTTPClient.Post(proxyURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("POST /v1/chat/completions: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("inference HTTP %d", resp.StatusCode)
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	return result.Choices[0].Message.Content, nil
}

func finalizeProxy(proxyURL string) (settlement, error) {
	resp, err := proxyHTTPClient.Post(proxyURL+"/v1/finalize", "application/json", nil)
	if err != nil {
		return settlement{}, fmt.Errorf("POST /v1/finalize: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return settlement{}, fmt.Errorf("finalize HTTP %d", resp.StatusCode)
	}
	var s settlement
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return settlement{}, fmt.Errorf("decode settlement: %w", err)
	}
	return s, nil
}
