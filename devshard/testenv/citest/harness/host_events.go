package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"devshard/testenv/config"
	"devshard/testenv/mockchain/adminface"

	"github.com/stretchr/testify/require"
)

// EscrowCreatedHostEvent is the mock-dapi /testenv/host-events/escrow-created body.
type EscrowCreatedHostEvent struct {
	EscrowID   uint64 `json:"escrow_id"`
	EpochIndex uint64 `json:"epoch_index,omitempty"`
	ModelID    string `json:"model_id,omitempty"`
	Creator    string `json:"creator,omitempty"`
	Amount     string `json:"amount,omitempty"`
}

// TriggerEscrowCreatedHostEvent posts an escrow-created host event to mock-dapi so
// the devshardd GetHostEvents long-poll warm caches the escrow metadata.
func TriggerEscrowCreatedHostEvent(t *testing.T, client *http.Client, mockDapiHTTP string, ev EscrowCreatedHostEvent) {
	t.Helper()
	postTestenvJSON(t, client, mockDapiHTTP+"/testenv/host-events/escrow-created", ev)
}

// SetEscrowQueryFault toggles mock-chain DevshardEscrow query failures via mock-dapi,
// simulating an unavailable request-time escrow fetch path.
func SetEscrowQueryFault(t *testing.T, client *http.Client, mockDapiHTTP string, faulted bool) {
	t.Helper()
	postTestenvJSON(t, client, mockDapiHTTP+"/testenv/escrow-query-fault", adminface.EscrowQueryFaultRequest{Faulted: faulted})
}

func postTestenvJSON(t *testing.T, client *http.Client, url string, payload any) {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "POST %s", url)
}

// PostgresEscrowCacheCount returns the number of devshard_escrow_cache rows for
// escrowID (the shared-Postgres warm cache written by the host-events sink).
func (s *Stack) PostgresEscrowCacheCount(cfg *config.File, escrowID string) (int, error) {
	if _, err := strconv.ParseUint(escrowID, 10, 64); err != nil {
		return 0, fmt.Errorf("escrow id %q is not numeric: %w", escrowID, err)
	}
	user, db, pass := postgresCreds(cfg)
	raw, err := s.ComposeExecOutput("devshard-postgres",
		"env", "PGPASSWORD="+pass,
		"psql", "-U", user, "-d", db, "-At",
		"-c", fmt.Sprintf("SELECT COUNT(*) FROM devshard_escrow_cache WHERE escrow_id = '%s'", escrowID))
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("parse escrow cache count %q: %w", raw, err)
	}
	return n, nil
}
