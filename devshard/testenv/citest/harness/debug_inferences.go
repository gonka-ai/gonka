package harness

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// GatewayInference is one record from GET /v1/debug/inferences (live + sealed).
type GatewayInference struct {
	Status       string `json:"status"`
	ExecutorSlot uint32 `json:"executor_slot"`
	VotesValid   uint32 `json:"votes_valid"`
	VotesInvalid uint32 `json:"votes_invalid"`
}

type gatewayDebugInferencesBody struct {
	Total      int                         `json:"total_inferences"`
	Inferences map[string]GatewayInference `json:"inferences"`
}

// GetGatewayInferences dumps every inference the gateway state machine knows.
func GetGatewayInferences(t *testing.T, client *http.Client, gatewayURL, adminAPIKey string) map[string]GatewayInference {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	var body gatewayDebugInferencesBody
	require.NoError(t, getGatewayJSON(t, client, gatewayURL+"/v1/debug/inferences", adminAPIKey, &body))
	if body.Inferences == nil {
		return map[string]GatewayInference{}
	}
	return body.Inferences
}

// CountGatewayInferenceStatus counts records in the given status.
func CountGatewayInferenceStatus(infs map[string]GatewayInference, status string) int {
	n := 0
	for _, rec := range infs {
		if rec.Status == status {
			n++
		}
	}
	return n
}

// WaitGatewayInferenceStatus polls /v1/debug/inferences until at least min
// records are in status, or timeout.
func WaitGatewayInferenceStatus(t *testing.T, client *http.Client, gatewayURL, adminAPIKey, status string, min int, timeout time.Duration) map[string]GatewayInference {
	t.Helper()
	if client == nil {
		client = GatewayChatClient()
	}
	deadline := time.Now().Add(timeout)
	var last map[string]GatewayInference
	for time.Now().Before(deadline) {
		last = GetGatewayInferences(t, client, gatewayURL, adminAPIKey)
		if CountGatewayInferenceStatus(last, status) >= min {
			return last
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("citest: inferences with status %q: %d < %d after %s (total=%d challenged=%d invalidated=%d finished=%d)",
		status, CountGatewayInferenceStatus(last, status), min, timeout, len(last),
		CountGatewayInferenceStatus(last, "challenged"),
		CountGatewayInferenceStatus(last, "invalidated"),
		CountGatewayInferenceStatus(last, "finished"))
	return last
}
