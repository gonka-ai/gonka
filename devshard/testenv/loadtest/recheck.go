package loadtest

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"devshard/testenv/mockchain/ledgerface"
)

const (
	runInputsFile = "run-inputs.json"
	notSaved      = "not saved by this run (no run-inputs.json)"
)

// RunInputs is what the checks read from the live stack after the workload. A run saves it to
// run-inputs.json, so EvaluateSavedRun judges the run directory again without a stack.
type RunInputs struct {
	// Started is the workload start the invariants and the ghost-rate fault windows are planned against.
	Started            time.Time          `json:"started"`
	Hosts              []InvariantHost    `json:"hosts"`
	Allocations        map[string]uint64  `json:"allocations"`
	InitialEscrowIDs   []string           `json:"initial_escrow_ids"`
	CurrentEscrowIDs   []string           `json:"current_escrow_ids"`
	CurrentEscrowError string             `json:"current_escrow_error,omitempty"`
	Orphans            *orphanPoll        `json:"orphans"`
	Burns              []gatewayBurn      `json:"burns"`
	Accounting         *AccountingReport  `json:"accounting"`
	AccountingError    string             `json:"accounting_error,omitempty"`
	Invariants         *invariantEndState `json:"invariants"`
}

// invariantEndState is what the invariants read once after drain, from the stack and the logs.
// Nil fields were not read; the evaluators report them as unavailable.
type invariantEndState struct {
	// MetricsBytes is the prefix of metrics.jsonl the evaluation reads; the collector appends after it.
	MetricsBytes       int64                                `json:"metrics_bytes"`
	SettlementDisabled map[string]bool                      `json:"settlement_disabled"`
	ReconcileTicks     map[string][]time.Time               `json:"reconcile_ticks"`
	Devshards          []GatewayDevshard                    `json:"devshards"`
	Inferences         map[string]map[string]DebugInference `json:"inferences"`
	SettleTxs          []ledgerface.TxView                  `json:"settle_txs"`
	Escrows            []ledgerface.EscrowView              `json:"escrows"`
	ChainError         string                               `json:"chain_error,omitempty"`
	Leases             []LeaseCount                         `json:"leases"`
	LeasesError        string                               `json:"leases_error,omitempty"`
	LeaseExcluded      map[string]string                    `json:"lease_excluded"`
	EscrowMissing      []string                             `json:"escrow_missing"`
	DidNotCompress     int                                  `json:"did_not_compress"`
	PayloadFiles       map[string]PayloadFileStats          `json:"payload_files"`
}

func writeRunInputs(dir string, inputs RunInputs) error {
	body, err := json.Marshal(inputs)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, runInputsFile), append(body, '\n'), 0o644)
}

// EvaluateSavedRun judges every check of a saved run again from its directory, without a stack,
// and rewrites its assertions.json and invariants.json. A run without run-inputs.json is judged
// from what its other files hold; the notes name what could not be recomputed.
func EvaluateSavedRun(dir string, strictXFail bool) (bool, []string, error) {
	checks, notes, err := recheckRun(dir, strictXFail)
	if err != nil {
		return false, notes, err
	}
	var verdict error
	if failures := failedChecks(checks); len(failures) > 0 {
		verdict = &assertionFailures{Checks: failures}
	}
	return verdict == nil, notes, writeAssertions(dir, checks, verdict)
}

// recheckRun is the one evaluation both the live run and EvaluateSavedRun use: it reads the
// scenario, the inputs and the artifacts back from dir.
func recheckRun(dir string, strictXFail bool) ([]AssertionResult, []string, error) {
	scenario, err := LoadScenario(filepath.Join(dir, "run.yaml"))
	if err != nil {
		return nil, nil, err
	}
	var summary Summary
	if err := readReportArtifact(dir, "summary.json", true, &summary); err != nil {
		return nil, nil, err
	}
	var events []EventRecord
	if err := readReportArtifact(dir, "events.json", false, &events); err != nil {
		return nil, nil, err
	}
	var inputs RunInputs
	var notes []string
	if _, err := os.Stat(filepath.Join(dir, runInputsFile)); errors.Is(err, os.ErrNotExist) {
		inputs, notes = legacyRunInputs(dir, scenario, summary)
	} else if err := readReportArtifact(dir, runInputsFile, true, &inputs); err != nil {
		return nil, nil, err
	}
	checks, err := evaluateRun(dir, scenario, summary, events, inputs, strictXFail)
	return checks, notes, err
}

func evaluateRun(dir string, scenario Scenario, summary Summary, events []EventRecord, inputs RunInputs, strictXFail bool) ([]AssertionResult, error) {
	checks := assertRun(scenario, summary, inputs, events)
	checks = append(checks, eventAssertions(events)...)
	if accountingEnabled(scenario.Assertions.Accounting) {
		if inputs.Accounting == nil {
			checks = append(checks, AssertionResult{Name: "accounting.snapshot", Expected: "final accounting snapshot and target participant available", Actual: "unavailable", Details: inputs.AccountingError})
		} else {
			checks = append(checks, accountingAssertions(scenario.Assertions.Accounting, *inputs.Accounting)...)
		}
	}
	if scenario.Assertions.Invariants.Enabled {
		invariantChecks, err := evaluateRunInvariants(dir, scenario, events, inputs)
		if err != nil {
			return nil, err
		}
		checks = append(checks, invariantChecks...)
	}
	if len(scenario.Cases) > 0 {
		checks = markKnownBugs(checks, scenario.Cases, strictXFail)
	}
	return checks, nil
}

func evaluateRunInvariants(dir string, scenario Scenario, events []EventRecord, inputs RunInputs) ([]AssertionResult, error) {
	end := inputs.Invariants
	if end == nil {
		end = &invariantEndState{DidNotCompress: -1, ChainError: notSaved, LeasesError: notSaved}
	}
	in := InvariantInputs{
		Started: inputs.Started, Workload: scenario.Duration(), Settings: resolveInvariantSettings(scenario), Hosts: inputs.Hosts,
		NodeHosts: map[string]string{}, Events: events, Burns: inputs.Burns, ChainMaxNonce: scenario.Topology.Chain.MaxNonce,
		SettlementDisabled: end.SettlementDisabled, ReconcileTicks: end.ReconcileTicks, Devshards: end.Devshards, Inferences: end.Inferences,
		SettleTxs: end.SettleTxs, Escrows: end.Escrows, ChainError: end.ChainError, Leases: end.Leases, LeasesError: end.LeasesError,
		LeaseExcluded: end.LeaseExcluded, EscrowMissing: end.EscrowMissing, DidNotCompress: end.DidNotCompress, PayloadFiles: end.PayloadFiles,
	}
	for _, node := range scenario.Topology.MockML.Nodes {
		in.NodeHosts[node.Name] = node.Host
	}
	var err error
	if in.Requests, err = readLadderRequests(filepath.Join(dir, "requests.jsonl")); err != nil {
		return nil, fmt.Errorf("read requests.jsonl: %w", err)
	}
	in.Accounting = readSamples[AccountingSnapshot](dir, "accounting-snapshots.jsonl")
	in.Throttle = readSamples[ThrottleSample](dir, "throttle.jsonl")
	in.Pending = readSamples[PendingSample](dir, "pending.jsonl")
	in.HostStats = readSamples[HostStatsSample](dir, "host-stats.jsonl")
	in.DapiHosts = readSamples[DapiHostSample](dir, "dapi-host-stats.jsonl")
	if in.Metrics, err = readMetricsSamplesUpTo(dir, end.MetricsBytes); err != nil {
		log.Printf("loadtest: invariants read metrics.jsonl: %v", err)
	}
	checks, report := evaluateInvariants(in)
	log.Printf("loadtest: stage=harness_health stalls=%d invalid_fraction=%s invalid_checks=%d", len(report.Health.Stalls), formatInvalidFraction(report.Health.InvalidFraction), len(report.Health.InvalidChecks))
	for _, stall := range report.Health.Stalls {
		log.Printf("loadtest: harness stall %s %s..%s gap=%s period=%s", stall.Source, stall.From.Format(time.RFC3339), stall.To.Format(time.RFC3339), stall.Gap, stall.Period)
	}
	if err := writeInvariantReport(dir, report); err != nil {
		log.Printf("loadtest: write invariants.json: %v", err)
	}
	return checks, nil
}

// readSamples reads a poller's JSONL artifact; a missing or unreadable file yields what it holds.
func readSamples[T any](dir, name string) []T {
	file, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("loadtest: invariants read %s: %v", name, err)
		}
		return nil
	}
	defer file.Close()
	var samples []T
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for scanner.Scan() {
		var sample T
		if err := json.Unmarshal(scanner.Bytes(), &sample); err != nil {
			log.Printf("loadtest: invariants read %s: %v", name, err)
			return samples
		}
		samples = append(samples, sample)
	}
	if err := scanner.Err(); err != nil {
		log.Printf("loadtest: invariants read %s: %v", name, err)
	}
	return samples
}

// readMetricsSamplesUpTo reads the first limit bytes of metrics.jsonl, or all of it when limit is 0.
func readMetricsSamplesUpTo(dir string, limit int64) ([]MetricsSample, error) {
	file, err := os.Open(filepath.Join(dir, "metrics.jsonl"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var reader io.Reader = file
	if limit > 0 {
		reader = io.LimitReader(file, limit)
	}
	var samples []MetricsSample
	decoder := json.NewDecoder(reader)
	for {
		var sample MetricsSample
		if err := decoder.Decode(&sample); err == io.EOF {
			return samples, nil
		} else if err != nil {
			return samples, err
		}
		samples = append(samples, sample)
	}
}

// completeLinesSize is the size of path up to its last newline: the records the collector has
// finished writing.
func completeLinesSize(path string) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	buf := make([]byte, 64<<10)
	for end := info.Size(); end > 0; {
		start := max(0, end-int64(len(buf)))
		n, err := file.ReadAt(buf[:end-start], start)
		if err != nil && err != io.EOF {
			return 0, err
		}
		for i := n - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				return start + int64(i) + 1, nil
			}
		}
		end = start
	}
	return 0, nil
}

// legacyRunInputs rebuilds what it can of the inputs of a run saved before run-inputs.json, and
// names the rest.
func legacyRunInputs(dir string, scenario Scenario, summary Summary) (RunInputs, []string) {
	inputs := RunInputs{Started: summary.StartedAt}
	notes := []string{"invariants are judged from summary.json started_at, a moment after the invariant pollers started"}
	seen := map[string]bool{}
	for _, s := range readSamples[HostStatsSample](dir, "host-stats.jsonl") {
		if s.Host != "" && s.Participant != "" && !seen[s.Host] {
			seen[s.Host] = true
			inputs.Hosts = append(inputs.Hosts, InvariantHost{ID: s.Host, Participant: s.Participant})
		}
	}
	sort.Slice(inputs.Hosts, func(i, j int) bool { return inputs.Hosts[i].ID < inputs.Hosts[j].ID })
	notes = append(notes, fmt.Sprintf("host-to-participant map rebuilt from host-stats.jsonl: %d hosts", len(inputs.Hosts)))
	var mlStats map[string]struct {
		Allocations uint64 `json:"allocations"`
	}
	if err := readReportArtifact(dir, "ml-stats.json", false, &mlStats); err == nil && mlStats != nil {
		inputs.Allocations = map[string]uint64{}
		for node, stats := range mlStats {
			inputs.Allocations[node] = stats.Allocations
		}
		notes = append(notes, "mock_ml.node_used reads the allocations in ml-stats.json, taken after the drain")
	}
	if scenario.Assertions.EscrowRotation.RequireNewEscrow {
		inputs.CurrentEscrowError = notSaved
		notes = append(notes, "gateway.escrow_rotation.new_escrow: escrow IDs "+notSaved)
	}
	if scenario.Assertions.Devshard.NoOrphanedWork {
		notes = append(notes, "devshard.no_orphaned_work: the drain check's last read "+notSaved)
	}
	var report AccountingReport
	if err := readReportArtifact(dir, "accounting.json", false, &report); err == nil && report.TargetParticipant != "" {
		inputs.Accounting = &report
	} else {
		inputs.AccountingError = notSaved
	}
	end := &invariantEndState{DidNotCompress: -1, ChainError: notSaved, LeasesError: notSaved, SettlementDisabled: settlementDisabledModels(scenario, scenario.RequestModels())}
	notes = append(notes, "gateway devshards and inferences, chain ledger and Postgres leases "+notSaved)
	logPath := filepath.Join(dir, "compose-pre-drain.log")
	if burns, err := readGatewayBurns(logPath); err == nil {
		inputs.Burns = burns
		end.DidNotCompress, _ = countLogLines(logPath, "did not compress")
		end.ReconcileTicks, _ = readReconcileTicks(logPath)
		end.EscrowMissing, _ = readEscrowMissing(logPath)
	} else {
		notes = append(notes, "compose-pre-drain.log not readable (decompress compose-pre-drain.log.gz first): no burns, reconcile ticks, escrow-missing or did-not-compress lines")
	}
	for _, host := range zstdPayloadHosts(scenario) {
		if end.PayloadFiles == nil {
			end.PayloadFiles, end.LeaseExcluded = map[string]PayloadFileStats{}, map[string]string{}
		}
		end.PayloadFiles[host] = scanPayloadDir(filepath.Join(dir, "payload-sample", host))
		end.LeaseExcluded[host] = sqliteLeaseExclusion
	}
	inputs.Invariants = end
	return inputs, notes
}
