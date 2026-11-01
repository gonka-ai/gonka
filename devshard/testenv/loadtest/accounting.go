package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"devshard/testenv/config"
)

const timeoutAppliedOutcome = "applied"

type accountingAPIResponse struct {
	EpochIndex   uint64                  `json:"epoch_index"`
	Participants []accountingParticipant `json:"participants"`
}

type accountingParticipant struct {
	Participant     string                `json:"participant"`
	Model           string                `json:"model"`
	AssignedNonces  uint64                `json:"assigned_nonces"`
	ProtocolMisses  uint64                `json:"protocol_misses"`
	TimeoutOutcomes map[string]uint64     `json:"timeout_outcomes"`
	CrossChecks     accountingCrossChecks `json:"cross_checks"`
}

type accountingCrossChecks struct {
	TimeoutApplied uint64 `json:"timeout_applied"`
	HostMissed     uint64 `json:"host_missed"`
	ErrorCount     uint64 `json:"error_count"`
}

type accountingTotals struct {
	Participant      string `json:"participant,omitempty"`
	Model            string `json:"model,omitempty"`
	AssignedNonces   uint64 `json:"assigned_nonces"`
	ProtocolMisses   uint64 `json:"protocol_misses"`
	TimeoutApplied   uint64 `json:"timeout_applied"`
	HostMissed       uint64 `json:"host_missed"`
	CrossCheckErrors uint64 `json:"cross_check_errors"`
}

// AccountingReport contains the before/after counters used by protocol-fault
// scenarios. The miss rate is derived from protocol misses over newly assigned
// nonces, not from Mock ML HTTP failures.
type AccountingReport struct {
	TargetHost        string           `json:"target_host,omitempty"`
	TargetParticipant string           `json:"target_participant,omitempty"`
	Initial           accountingTotals `json:"initial"`
	Final             accountingTotals `json:"final"`
	Delta             accountingTotals `json:"delta"`
	MissRate          float64          `json:"miss_rate"`
}

func accountingEnabled(assertions AccountingAssertions) bool {
	return assertions.TargetHost != "" || assertions.MinProtocolMisses > 0 || assertions.MinTimeoutsApplied > 0 || assertions.RequireCrossChecks
}

func fetchAccountingSnapshot(ctx context.Context, statsURL string) (accountingAPIResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, statsURL+"/api/v1/epochs/current/participants", nil)
	if err != nil {
		return accountingAPIResponse{}, err
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return accountingAPIResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return accountingAPIResponse{}, fmt.Errorf("accounting endpoint returned %s", response.Status)
	}
	var snapshot accountingAPIResponse
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		return accountingAPIResponse{}, err
	}
	return snapshot, nil
}

func waitForAccountingSnapshot(ctx context.Context, statsURL, phase string) (accountingAPIResponse, error) {
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		snapshot, err := fetchAccountingSnapshot(ctx, statsURL)
		if err == nil && len(snapshot.Participants) > 0 {
			return snapshot, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("accounting returned no participants during %s", phase)
		}
		select {
		case <-ctx.Done():
			return accountingAPIResponse{}, ctx.Err()
		case <-deadline.C:
			return accountingAPIResponse{}, lastErr
		case <-ticker.C:
		}
	}
}

func accountingTarget(cfg *config.File, targetHost string) (string, error) {
	if targetHost == "" {
		return "", nil
	}
	for _, host := range cfg.Hosts {
		if host.ID == targetHost {
			if host.Address == "" {
				return "", fmt.Errorf("accounting target host %s has no participant address", targetHost)
			}
			return host.Address, nil
		}
	}
	return "", fmt.Errorf("accounting target host %s is absent from generated config", targetHost)
}

func sumAccounting(snapshot accountingAPIResponse, participant string) accountingTotals {
	totals := accountingTotals{Participant: participant}
	for _, record := range snapshot.Participants {
		if participant != "" && record.Participant != participant {
			continue
		}
		totals.Model = record.Model
		totals.AssignedNonces += record.AssignedNonces
		totals.ProtocolMisses += record.ProtocolMisses
		totals.TimeoutApplied += record.TimeoutOutcomes[timeoutAppliedOutcome]
		totals.HostMissed += record.CrossChecks.HostMissed
		totals.CrossCheckErrors += record.CrossChecks.ErrorCount
	}
	return totals
}

func subtractAccounting(final, initial accountingTotals) accountingTotals {
	return accountingTotals{
		Participant:      final.Participant,
		Model:            final.Model,
		AssignedNonces:   final.AssignedNonces - initial.AssignedNonces,
		ProtocolMisses:   final.ProtocolMisses - initial.ProtocolMisses,
		TimeoutApplied:   final.TimeoutApplied - initial.TimeoutApplied,
		HostMissed:       final.HostMissed - initial.HostMissed,
		CrossCheckErrors: final.CrossCheckErrors - initial.CrossCheckErrors,
	}
}

func buildAccountingReport(cfg *config.File, targetHost string, initial, final accountingAPIResponse) (AccountingReport, error) {
	participant, err := accountingTarget(cfg, targetHost)
	if err != nil {
		return AccountingReport{}, err
	}
	report := AccountingReport{
		TargetHost:        targetHost,
		TargetParticipant: participant,
		Initial:           sumAccounting(initial, participant),
		Final:             sumAccounting(final, participant),
	}
	report.Delta = subtractAccounting(report.Final, report.Initial)
	if report.Delta.AssignedNonces > 0 {
		report.MissRate = float64(report.Delta.ProtocolMisses) / float64(report.Delta.AssignedNonces)
	}
	return report, nil
}

func accountingAssertions(assertions AccountingAssertions, report AccountingReport) []AssertionResult {
	checks := make([]AssertionResult, 0, 3)
	if assertions.MinProtocolMisses > 0 {
		checks = append(checks, AssertionResult{
			Name:     "accounting.protocol_misses",
			Expected: fmt.Sprintf("delta >= %d", assertions.MinProtocolMisses),
			Actual:   fmt.Sprintf("delta=%d miss_rate=%.2f%%", report.Delta.ProtocolMisses, report.MissRate*100),
			Details:  fmt.Sprintf("assigned_delta=%d target=%s", report.Delta.AssignedNonces, report.TargetParticipant),
			Passed:   report.Delta.ProtocolMisses >= assertions.MinProtocolMisses,
		})
	}
	if assertions.MinTimeoutsApplied > 0 {
		checks = append(checks, AssertionResult{
			Name:     "accounting.timeout_outcomes.applied",
			Expected: fmt.Sprintf("delta >= %d", assertions.MinTimeoutsApplied),
			Actual:   fmt.Sprintf("delta=%d", report.Delta.TimeoutApplied),
			Details:  "an applied timeout is the protocol event that charges the miss",
			Passed:   report.Delta.TimeoutApplied >= assertions.MinTimeoutsApplied,
		})
	}
	if assertions.RequireCrossChecks {
		consistent := report.Delta.TimeoutApplied == report.Delta.ProtocolMisses &&
			report.Delta.HostMissed == report.Delta.ProtocolMisses &&
			report.Delta.CrossCheckErrors == 0
		checks = append(checks, AssertionResult{
			Name:     "accounting.miss_cross_checks",
			Expected: "timeout_applied == protocol_misses == host_missed; errors=0",
			Actual:   fmt.Sprintf("timeout_applied=%d protocol_misses=%d host_missed=%d errors=%d", report.Delta.TimeoutApplied, report.Delta.ProtocolMisses, report.Delta.HostMissed, report.Delta.CrossCheckErrors),
			Passed:   consistent,
		})
	}
	return checks
}

func appendAssertionFailure(existing error, check AssertionResult) error {
	if check.Passed {
		return existing
	}
	if existing == nil {
		return &assertionFailures{Checks: []AssertionResult{check}}
	}
	if failures, ok := existing.(*assertionFailures); ok {
		failures.Checks = append(failures.Checks, check)
		return failures
	}
	return existing
}

func writeAccountingReport(outputDir string, report AccountingReport) error {
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, "accounting.json"), append(body, '\n'), 0o644)
}
