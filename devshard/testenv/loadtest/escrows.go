package loadtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxScenarioEscrows bounds topology.chain.escrows: every escrow is a session on every host.
const maxScenarioEscrows = 16

// escrowsReadyTimeout is how long the runner waits for the gateway to hold the escrows it created.
const escrowsReadyTimeout = 3 * time.Minute

// addScenarioEscrows makes the gateway hold total escrows of model: it creates the ones beyond the
// configured escrow through POST /v1/admin/escrows (registered, as an operator does) and waits until
// GET /v1/admin/devshards lists them all. It returns the ids it created.
func addScenarioEscrows(ctx context.Context, gatewayURL, apiKey, model string, amount uint64, total int, wait time.Duration) ([]string, error) {
	if total <= 1 {
		return nil, nil
	}
	client := &http.Client{Timeout: time.Minute}
	var created []string
	for len(created) < total-1 {
		id, err := createGatewayEscrow(ctx, client, gatewayURL, apiKey, model, amount)
		if err != nil {
			return created, fmt.Errorf("create escrow %d of %d: %w", len(created)+2, total, err)
		}
		created = append(created, id)
	}
	deadline := time.Now().Add(wait)
	for {
		ids, err := fetchGatewayDevshardIDs(ctx, gatewayURL, apiKey)
		held := map[string]bool{}
		for _, id := range ids {
			held[id] = true
		}
		missing := 0
		for _, id := range created {
			if !held[id] {
				missing++
			}
		}
		if err == nil && missing == 0 {
			return created, nil
		}
		if time.Now().After(deadline) {
			return created, fmt.Errorf("gateway holds %d of %d escrows after %s (last list error: %v)", total-missing, total, wait, err)
		}
		select {
		case <-ctx.Done():
			return created, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func createGatewayEscrow(ctx context.Context, client *http.Client, gatewayURL, apiKey, model string, amount uint64) (string, error) {
	payload, err := json.Marshal(map[string]any{"amount": amount, "model_id": model, "register": true})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL+"/v1/admin/escrows", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("POST /v1/admin/escrows: %w", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("POST /v1/admin/escrows returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var out struct {
		EscrowID uint64 `json:"escrow_id"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.EscrowID == 0 {
		return "", fmt.Errorf("POST /v1/admin/escrows answered without an escrow id: %s", strings.TrimSpace(string(body)))
	}
	return strconv.FormatUint(out.EscrowID, 10), nil
}

// severalRuntimes says whether the gateway holds more than one runtime during the run: rotation adds
// them as it goes, topology.chain.escrows starts with several. The gateway root then answers the
// debug routes for none of them, so their state is read escrow by escrow.
func (s Scenario) severalRuntimes() bool {
	return s.Gateway.EscrowRotation.Enabled || s.Topology.Chain.Escrows > 1
}
