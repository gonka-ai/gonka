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
	Participant           string            `json:"participant"`
	Model                 string            `json:"model"`
	AssignedNonces        uint64            `json:"assigned_nonces"`
	Dispositions          map[string]uint64 `json:"dispositions"`
	TimeoutOutcomes       map[string]uint64 `json:"timeout_outcomes"`
	ProtocolMisses        uint64            `json:"protocol_misses"`
	InFlight              uint64            `json:"in_flight"`
	TimeoutPending        uint64            `json:"timeout_pending"`
	PendingClassification uint64            `json:"pending_classification"`
	Unclassified          uint64            `json:"unclassified"`
	Overclassified        uint64            `json:"overclassified"`
	CrossChecks           AccountingChecks  `json:"cross_checks"`
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

func AccountingDispositionCount(resp AccountingParticipantsResponse, disposition string) uint64 {
	var total uint64
	for _, participant := range resp.Participants {
		total += participant.Dispositions[disposition]
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
