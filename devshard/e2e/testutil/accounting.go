package testutil

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type AccountingParticipantsResponse struct {
	SchemaVersion   int                     `json:"schema_version"`
	RecordingErrors uint64                  `json:"recording_errors"`
	WriterErrors    uint64                  `json:"writer_errors"`
	EpochIndex      uint64                  `json:"epoch_index"`
	Participants    []AccountingParticipant `json:"participants"`
}

type AccountingParticipant struct {
	Participant           string              `json:"participant"`
	Model                 string              `json:"model"`
	AssignedNonces        uint64              `json:"assigned_nonces"`
	Dispositions          map[string]uint64   `json:"dispositions"`
	TimeoutOutcomes       map[string]uint64   `json:"timeout_outcomes"`
	ProtocolMisses        uint64              `json:"protocol_misses"`
	InFlight              uint64              `json:"in_flight"`
	TimeoutPending        uint64              `json:"timeout_pending"`
	PendingClassification uint64              `json:"pending_classification"`
	Unclassified          uint64              `json:"unclassified"`
	Overclassified        uint64              `json:"overclassified"`
	ChainCost             uint64              `json:"chain_cost"`
	ReservedCost          uint64              `json:"reserved_cost"`
	ActualCost            uint64              `json:"actual_cost"`
	RefundedCost          uint64              `json:"refunded_cost"`
	InputTokens           uint64              `json:"input_tokens"`
	OutputTokens          uint64              `json:"output_tokens"`
	ServiceNonces         map[string]uint64   `json:"service_nonces"`
	CrossChecks           AccountingChecks    `json:"cross_checks"`
	Counters              []AccountingCounter `json:"counters"`
}

type AccountingCounter struct {
	EscrowID string               `json:"escrow_id"`
	Key      AccountingCounterKey `json:"key"`
	Count    uint64               `json:"count"`
}

type AccountingCounterKey struct {
	SlotID                 uint32 `json:"slot_id"`
	Disposition            string `json:"disposition"`
	DispatchPhase          string `json:"dispatch_phase,omitempty"`
	TimeoutEvaluationPhase string `json:"timeout_evaluation_phase,omitempty"`
	QuarantineMode         string `json:"quarantine_mode,omitempty"`
	NoSendReason           string `json:"no_send_reason,omitempty"`
	GhostReason            string `json:"ghost_reason,omitempty"`
	FailureOrigin          string `json:"failure_origin,omitempty"`
	DetailReason           string `json:"detail_reason,omitempty"`
	TimeoutKind            string `json:"timeout_kind,omitempty"`
	TimeoutOutcome         string `json:"timeout_outcome,omitempty"`
	TimeoutReason          string `json:"timeout_reason,omitempty"`
}

// UnmarshalJSON reads devshardctl's counter, keyed under "key", and the gateway's, keyed flat beside the count.
func (c *AccountingCounter) UnmarshalJSON(data []byte) error {
	var nested struct {
		EscrowID string                `json:"escrow_id"`
		Key      *AccountingCounterKey `json:"key"`
		Count    uint64                `json:"count"`
	}
	if err := json.Unmarshal(data, &nested); err != nil {
		return err
	}
	c.EscrowID, c.Count = nested.EscrowID, nested.Count
	if nested.Key != nil {
		c.Key = *nested.Key
		return nil
	}
	return json.Unmarshal(data, &c.Key)
}

type AccountingChecks struct {
	TimeoutApplied  uint64 `json:"timeout_applied"`
	HostMissed      uint64 `json:"host_missed"`
	RecordedInvalid uint64 `json:"recorded_invalid_transitions"`
	HostInvalid     uint64 `json:"host_invalid"`
	ErrorCount      uint64 `json:"error_count"`
}

func WaitAccountingParticipants(
	t *testing.T,
	client *http.Client,
	statsURL string,
	query string,
	ready func(AccountingParticipantsResponse) bool,
) AccountingParticipantsResponse {
	t.Helper()
	deadline := time.Now().Add(DefaultRequestTimeout)
	var last AccountingParticipantsResponse
	var lastStatus int
	var lastBody string
	var lastErr error

	for time.Now().Before(deadline) {
		last, lastStatus, lastBody, lastErr = fetchAccountingParticipants(t, client, statsURL, query)
		if lastErr == nil && lastStatus >= http.StatusOK && lastStatus < http.StatusMultipleChoices {
			if ready == nil || ready(last) {
				return last
			}
		}
		time.Sleep(250 * time.Millisecond)
	}

	require.NoError(t, lastErr)
	require.GreaterOrEqual(t, lastStatus, http.StatusOK, "GET accounting participants: %s", lastBody)
	require.Less(t, lastStatus, http.StatusMultipleChoices, "GET accounting participants: %s", lastBody)
	if ready != nil {
		require.True(t, ready(last), "accounting participants did not reach expected state: %+v", last)
	}
	return last
}

func fetchAccountingParticipants(
	t *testing.T,
	client *http.Client,
	statsURL string,
	query string,
) (AccountingParticipantsResponse, int, string, error) {
	t.Helper()
	endpoint := strings.TrimRight(statsURL, "/") + "/api/v1/epochs/current/participants"
	if query != "" {
		endpoint += "?" + query
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return AccountingParticipantsResponse{}, 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+AdminAPIKey)
	DebugLogf(t, "GET %s", endpoint)

	resp, err := client.Do(req)
	if err != nil {
		return AccountingParticipantsResponse{}, 0, "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return AccountingParticipantsResponse{}, resp.StatusCode, "", err
	}
	DebugLogf(t, "GET %s status=%d response=%s", endpoint, resp.StatusCode, string(body))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return AccountingParticipantsResponse{}, resp.StatusCode, string(body), nil
	}

	var decoded AccountingParticipantsResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return AccountingParticipantsResponse{}, resp.StatusCode, string(body), fmt.Errorf("decode accounting response: %w", err)
	}
	return decoded, resp.StatusCode, string(body), nil
}

func AccountingAssignedTotal(resp AccountingParticipantsResponse) uint64 {
	var total uint64
	for _, participant := range resp.Participants {
		total += participant.AssignedNonces
	}
	return total
}

func AccountingDispositionTotal(resp AccountingParticipantsResponse) uint64 {
	var total uint64
	for _, participant := range resp.Participants {
		total += AccountingParticipantDispositionTotal(participant)
	}
	return total
}

// SettledAccounting polls until nothing is in flight or awaiting a timeout vote, and returns the last reading either way; expectDispositions also waits for the served traffic to be classified.
func SettledAccounting(t *testing.T, client *http.Client, statsURL, query string, within time.Duration, expectDispositions bool) AccountingParticipantsResponse {
	t.Helper()
	deadline := time.Now().Add(within)
	var last AccountingParticipantsResponse
	for {
		reading, status, _, err := fetchAccountingParticipants(t, client, statsURL, query)
		if err == nil && status == http.StatusOK {
			last = reading
			classified := !expectDispositions || AccountingDispositionTotal(reading) > 0
			if classified && AccountingInFlightTotal(reading) == 0 && accountingTimeoutPendingTotal(reading) == 0 {
				return reading
			}
		}
		if time.Now().After(deadline) {
			return last
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func accountingTimeoutPendingTotal(resp AccountingParticipantsResponse) uint64 {
	var total uint64
	for _, participant := range resp.Participants {
		total += participant.TimeoutPending
	}
	return total
}

// AccountingTimeoutOutcomes sums every participant's timeout outcomes into one map.
func AccountingTimeoutOutcomes(resp AccountingParticipantsResponse) map[string]uint64 {
	totals := make(map[string]uint64)
	for _, participant := range resp.Participants {
		for outcome, count := range participant.TimeoutOutcomes {
			totals[outcome] += count
		}
	}
	return totals
}

// AccountingDispositions sums every participant's dispositions into one map.
func AccountingDispositions(resp AccountingParticipantsResponse) map[string]uint64 {
	totals := make(map[string]uint64)
	for _, participant := range resp.Participants {
		for disposition, count := range participant.Dispositions {
			totals[disposition] += count
		}
	}
	return totals
}

func AccountingDispositionCount(resp AccountingParticipantsResponse, disposition string) uint64 {
	var total uint64
	for _, participant := range resp.Participants {
		total += participant.Dispositions[disposition]
	}
	return total
}

func AccountingGhostCounterCount(resp AccountingParticipantsResponse, noSendReason, quarantineMode string) uint64 {
	var total uint64
	for _, participant := range resp.Participants {
		for _, counter := range participant.Counters {
			if counter.Key.Disposition != "ghost" {
				continue
			}
			if noSendReason != "" && counter.Key.NoSendReason != noSendReason && counter.Key.GhostReason != noSendReason {
				continue
			}
			if quarantineMode != "" && counter.Key.QuarantineMode != quarantineMode {
				continue
			}
			total += counter.Count
		}
	}
	return total
}

func AccountingTimeoutOutcomeCount(resp AccountingParticipantsResponse, outcome string) uint64 {
	var total uint64
	for _, participant := range resp.Participants {
		total += participant.TimeoutOutcomes[outcome]
	}
	return total
}

func AccountingProtocolMissTotal(resp AccountingParticipantsResponse) uint64 {
	var total uint64
	for _, participant := range resp.Participants {
		total += participant.ProtocolMisses
	}
	return total
}

func AccountingCrossCheckTimeoutAppliedTotal(resp AccountingParticipantsResponse) uint64 {
	var total uint64
	for _, participant := range resp.Participants {
		total += participant.CrossChecks.TimeoutApplied
	}
	return total
}

func AccountingCrossCheckHostMissedTotal(resp AccountingParticipantsResponse) uint64 {
	var total uint64
	for _, participant := range resp.Participants {
		total += participant.CrossChecks.HostMissed
	}
	return total
}

func AccountingParticipantDispositionTotal(participant AccountingParticipant) uint64 {
	var total uint64
	for _, count := range participant.Dispositions {
		total += count
	}
	return total
}

func AccountingInFlightTotal(resp AccountingParticipantsResponse) uint64 {
	var total uint64
	for _, participant := range resp.Participants {
		total += participant.InFlight
	}
	return total
}

func AccountingUnfinishedTotal(resp AccountingParticipantsResponse) uint64 {
	return AccountingDispositionCount(resp, "unfinished_refused") +
		AccountingDispositionCount(resp, "unfinished_execution")
}

func RequireAccountingResponseCoherent(t *testing.T, resp AccountingParticipantsResponse, expectedModel string) {
	t.Helper()
	require.NotZero(t, resp.SchemaVersion, "accounting response should declare a schema version")
	require.NotZero(t, resp.EpochIndex, "accounting response should resolve the current epoch")
	require.Zero(t, resp.RecordingErrors, "accounting recorder should not report dropped event errors")
	require.Zero(t, resp.WriterErrors, "accounting snapshot writer should not report errors")
	require.NotEmpty(t, resp.Participants, "accounting response should include participant records")

	for _, participant := range resp.Participants {
		require.NotEmpty(t, participant.Participant, "participant address should be populated")
		if expectedModel != "" {
			require.Equal(t, expectedModel, participant.Model)
		}
		require.Zero(t, participant.Overclassified, "participant %s should not overclassify assigned nonces", participant.Participant)
		require.Zero(t, participant.CrossChecks.ErrorCount, "participant %s should not have accounting cross-check errors", participant.Participant)
	}
}

func RequireNonceAccountingBalanced(t *testing.T, resp AccountingParticipantsResponse) {
	t.Helper()
	for _, participant := range resp.Participants {
		dispositionTotal := AccountingParticipantDispositionTotal(participant)
		accounted := dispositionTotal +
			participant.InFlight +
			participant.PendingClassification +
			participant.Unclassified

		require.Equal(t, participant.AssignedNonces, accounted,
			"participant %s model %s should account every assigned nonce exactly once: dispositions=%d in_flight=%d pending_classification=%d unclassified=%d assigned=%d",
			participant.Participant,
			participant.Model,
			dispositionTotal,
			participant.InFlight,
			participant.PendingClassification,
			participant.Unclassified,
			participant.AssignedNonces,
		)
		require.Zero(t, participant.Overclassified, "participant %s should not overclassify assigned nonces", participant.Participant)
	}
}
