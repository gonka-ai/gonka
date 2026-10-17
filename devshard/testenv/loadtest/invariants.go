package loadtest

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"devshard/testenv/mockchain/ledgerface"
)

const (
	defaultInvariantSteadyAfter = 2 * time.Minute
	// Sweep constants of the gateway: executionTimeoutSweepGrace plus
	// user.TimeoutBuffer, checked on the 30 s balance tick.
	gatewaySweepGrace = 2*time.Minute + 5*time.Second
	gatewaySweepTick  = 30 * time.Second
	// defaultChainPhasePollInterval of the gateway's phase gate: it admits
	// until its next poll sees the blocking phase.
	gatewayPhasePoll = 5 * time.Second

	quietErrorRate     = 0.005
	quietP50Factor     = 1.5
	quietP99Factor     = 3.0
	faultErrorRate     = 0.02
	recoveryBucket     = 30 * time.Second
	recoveryLimit      = 60 * time.Second
	restartRecoveryLim = 90 * time.Second
	goroutineGrowthMul = 1.2
	goroutineGrowthAdd = 50
	heapSlopeLimit     = float64(1 << 20)
	// heapFloorBucket: the heap slope is fitted over the lowest heap_alloc of each bucket, the live
	// heap after a GC; heap in use also holds garbage up to the GC goal, about twice the live heap.
	heapFloorBucket    = 30 * time.Second
	gatewayCacheFamily = "devshard_gateway_cache_bytes"
	// A host keeps one runtime per escrow until the epoch is evicted (devshardd EvictBefore), each
	// with host.defaultValidationWorkers goroutines and a defaultValidationQueueSize-slot queue of
	// 176-byte validateJob values, so it grows by these for every escrow the run creates.
	hostValidationWorkers    = 20
	hostValidationQueueBytes = 20_000 * 176
	// hostServingFamily counts the requests a host finished; a host is serving once it rises above 0.
	hostServingFamily   = "devshard_request_terminal_total"
	throttlePollEvery   = 5 * time.Second
	pendingPollEvery    = 10 * time.Second
	hostStatsPollEvery  = 30 * time.Second
	dapiHostsPollEvery  = 10 * time.Second
	stateRootRecovery   = 60 * time.Second
	quarantineDurationT = throttlePollEvery + time.Second
	// senderRateQuarantine is the gateway's pause for a sender-rate refusal
	// (peerQuotaQuarantine, reason sender_rate_quarantine, from #1988).
	senderRateQuarantine = time.Second
	// sqliteLeaseExclusion is why no-skipped-validation-leases skips a sqlite host: its lease store is a no-op (storage/leases.go).
	sqliteLeaseExclusion = "sqlite storage keeps no validation leases"
)

// invariantSettings are the resolved invariant bounds for one run.
type invariantSettings struct {
	Grace         time.Duration
	MaxPendingAge time.Duration
	SteadyAfter   time.Duration
	Baseline      [2]time.Duration
	TrendEarly    [2]time.Duration
	TrendLate     [2]time.Duration
	Cases         map[string]string
	// Quarantine holds configured durations by reason family:
	// http, transport, empty_stream, stalled_winner, sender_rate.
	Quarantine map[string]time.Duration
}

// resolveInvariantSettings applies the defaults: grace g = refusal + execution
// + sweep grace + sweep tick, because a stuck nonce is only voted timed out by
// the sweep that long after its window; the pending-age bound adds 30 s.
func resolveInvariantSettings(s Scenario) invariantSettings {
	refusal, _ := time.ParseDuration(s.Protocol.RefusalTimeout)
	execution, _ := time.ParseDuration(s.Protocol.ExecutionTimeout)
	inv := s.Assertions.Invariants
	workload := s.Duration()
	out := invariantSettings{
		Grace:         refusal + execution + gatewaySweepGrace + gatewaySweepTick,
		MaxPendingAge: refusal + execution + gatewaySweepGrace + gatewaySweepTick + 30*time.Second,
		SteadyAfter:   min(defaultInvariantSteadyAfter, workload/2),
		Cases:         inv.Cases,
		Quarantine:    map[string]time.Duration{"sender_rate": senderRateQuarantine},
	}
	if d, err := time.ParseDuration(inv.Grace); err == nil {
		out.Grace = d
	}
	if d, err := time.ParseDuration(inv.MaxPendingAge); err == nil {
		out.MaxPendingAge = d
	}
	out.Baseline = parseInvariantWindow(inv.Baseline, [2]time.Duration{min(time.Minute, workload/4), min(2*time.Minute, workload/2)})
	out.TrendEarly = parseInvariantWindow(inv.TrendEarly, [2]time.Duration{out.SteadyAfter, min(out.SteadyAfter+2*time.Minute, workload)})
	out.TrendLate = parseInvariantWindow(inv.TrendLate, [2]time.Duration{max(workload-2*time.Minute, out.SteadyAfter), workload})
	throttle := s.Gateway.ParticipantThrottle
	for family, value := range map[string]string{"http": throttle.HTTPQuarantine, "transport": throttle.TransportFailureQuarantine, "empty_stream": throttle.EmptyStreamQuarantine, "stalled_winner": throttle.StalledWinnerQuarantine} {
		if d, err := time.ParseDuration(value); err == nil {
			out.Quarantine[family] = d
		}
	}
	return out
}

func parseInvariantWindow(window InvariantWindow, fallback [2]time.Duration) [2]time.Duration {
	from, e1 := time.ParseDuration(window.From)
	to, e2 := time.ParseDuration(window.To)
	if e1 != nil || e2 != nil {
		return fallback
	}
	return [2]time.Duration{from, to}
}

// InvariantHost maps a versiond container to the on-chain participant it serves.
type InvariantHost struct {
	ID          string `json:"id"`
	Participant string `json:"participant"`
}

// InvariantInputs is everything the evaluators read; nil slices mean the
// source was not collected. Field comments name the source as in the design.
type InvariantInputs struct {
	Started            time.Time
	Workload           time.Duration
	Settings           invariantSettings
	Hosts              []InvariantHost                      // generated config
	NodeHosts          map[string]string                    // scenario mock_ml binding
	Events             []EventRecord                        // Events: events.json
	Requests           []RequestResult                      // Requests: requests.jsonl
	Accounting         []AccountingSnapshot                 // GW-acct: stats /api/v1/epochs/current/participants
	Throttle           []ThrottleSample                     // GW-dbg: /v1/admin/devshards/{id}/participants
	Pending            []PendingSample                      // GW-dbg: /v1/debug/pending + /v1/debug/inferences
	HostStats          []HostStatsSample                    // Host-stats: /stats/shards[/:id], /stats/rpc
	DapiHosts          []DapiHostSample                     // mock-dapi: /testenv/host-stats
	ReconcileTicks     map[string][]time.Time               // versiond "reconcile desired versions" lines
	Metrics            []MetricsSample                      // GW-prom and runtime: metrics.jsonl
	SettleTxs          []ledgerface.TxView                  // Chain: /testenv/txs?type=settle
	Escrows            []ledgerface.EscrowView              // Chain: /testenv/escrows
	Devshards          []GatewayDevshard                    // GW-dbg: /v1/admin/devshards after drain
	Inferences         map[string]map[string]DebugInference // GW-dbg: /v1/debug/inferences after drain
	SettlementDisabled map[string]bool                      // scenario escrow_rotation
	Leases             []LeaseCount                         // Postgres devshard_validation_leases
	LeasesError        string
	// LeaseExcluded maps a host no-skipped-validation-leases cannot see to the reason, e.g. a sqlite host.
	LeaseExcluded map[string]string
	ChainError    string
	// ChainMaxNonce is the mock-chain devshard max_nonce; zero skips escrow-accounting-reconciles.final_nonce_le_max.
	ChainMaxNonce uint32
	// EscrowMissing lists the escrows the gateway marked inactive as "escrow missing on chain"; nil when not read.
	EscrowMissing []string
	// DidNotCompress is the responses-compress count in compose-pre-drain.log; negative when unreadable.
	DidNotCompress int
	// PayloadFiles is the payload-files-are-zstd sample per sqlite host: docker exec tar of {data-dir}/payloads.
	PayloadFiles map[string]PayloadFileStats
	// Burns are the nonces the gateway committed and never sent, from compose-pre-drain.log.
	Burns []gatewayBurn
}

// timeSpan is a closed interval of wall-clock time.
type timeSpan struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

func (s timeSpan) contains(t time.Time) bool { return !t.Before(s.From) && !t.After(s.To) }

// overlapsAfter treats (from, to] as half-open: a delta between two snapshots
// belongs to a window that is still open after the earlier snapshot.
func (s timeSpan) overlapsAfter(from, to time.Time) bool {
	return s.To.After(from) && !s.From.After(to)
}

// faultWindow is one fired event attributed to a participant. Span is the
// real window; Graced extends its end by the grace period.
type faultWindow struct {
	Event       EventRecord
	Host        string
	Participant string
	Span        timeSpan
	Graced      timeSpan
	// Background windows yield to the other windows of their participant (see isBackground).
	Background bool
}

// invariantContext is the window and attribution engine over events.json.
type invariantContext struct {
	in           InvariantInputs
	hostToPart   map[string]string
	participants []string
	honest       map[string]bool
	windows      []faultWindow
	runEnd       time.Time
	// stalls are harness heartbeat gaps; invalid are their graced windows.
	stalls  []harnessStall
	invalid []timeSpan
}

func newInvariantContext(in InvariantInputs) *invariantContext {
	c := &invariantContext{in: in, hostToPart: map[string]string{}, honest: map[string]bool{}}
	c.runEnd = in.Started.Add(in.Workload)
	seen := map[string]bool{}
	for _, host := range in.Hosts {
		c.hostToPart[host.ID] = host.Participant
		if !seen[host.Participant] {
			seen[host.Participant] = true
			c.participants = append(c.participants, host.Participant)
		}
	}
	sort.Strings(c.participants)
	faulty := map[string]bool{}
	for _, record := range in.Events {
		host, participant := c.eventTarget(record)
		if participant != "" {
			faulty[participant] = true
		}
		if record.StartedAt == nil || participant == "" {
			continue
		}
		end := c.runEnd
		if record.EndedAt != nil {
			end = *record.EndedAt
		}
		span := timeSpan{From: *record.StartedAt, To: end}
		c.windows = append(c.windows, faultWindow{Event: record, Host: host, Participant: participant, Span: span, Graced: timeSpan{From: span.From, To: span.To.Add(in.Settings.Grace)}, Background: isBackground(record.Params)})
	}
	for _, participant := range c.participants {
		if !faulty[participant] {
			c.honest[participant] = true
		}
	}
	c.stalls = c.harnessStalls()
	c.invalid = invalidWindows(c.stalls, in.Settings.Grace)
	return c
}

// eventTarget resolves an event to its host and participant. Global events
// and ML nodes without a host binding attribute to nobody.
func (c *invariantContext) eventTarget(record EventRecord) (string, string) {
	switch eventActions[record.Action].target {
	case targetHost:
		return record.Target, c.hostToPart[record.Target]
	case targetMLNode:
		host := c.in.NodeHosts[record.Target]
		return host, c.hostToPart[host]
	}
	return "", ""
}

func (c *invariantContext) faultyDuring(participant string, from, to time.Time) []faultWindow {
	var out []faultWindow
	for _, w := range c.windows {
		if w.Participant == participant && w.Graced.overlapsAfter(from, to) {
			out = append(out, w)
		}
	}
	return out
}

func (c *invariantContext) honestList() []string {
	var out []string
	for _, p := range c.participants {
		if c.honest[p] {
			out = append(out, p)
		}
	}
	return out
}

func (c *invariantContext) runCase(invariant string) string {
	return c.in.Settings.Cases[invariant]
}

// checkCase lets one check, such as "escrow-accounting-reconciles.final_nonce_le_max", carry its own case.
func (c *invariantContext) checkCase(invariant, check string) string {
	if caseID, ok := c.in.Settings.Cases[invariant+"."+check]; ok {
		return caseID
	}
	return c.runCase(invariant)
}

// inForegroundWindow reports whether span overlaps a non-background window of participant.
func (c *invariantContext) inForegroundWindow(participant string, from, to time.Time) bool {
	for _, w := range c.faultyDuring(participant, from, to) {
		if !w.Background {
			return true
		}
	}
	return false
}

func invariantCheck(name, caseID string, passed bool, expected, actual, details string) AssertionResult {
	return AssertionResult{Name: "invariants." + name, Case: caseID, Passed: passed, Expected: expected, Actual: actual, Details: details}
}

// InvariantReport is written to invariants.json.
type InvariantReport struct {
	Grace   string            `json:"grace"`
	Honest  []string          `json:"honest"`
	Faulty  []faultWindowView `json:"faulty_windows"`
	Results []AssertionResult `json:"results"`
	Tables  map[string]any    `json:"tables"`
	Health  harnessHealth     `json:"harness_health"`
}

type faultWindowView struct {
	Event       string    `json:"event"`
	Case        string    `json:"case,omitempty"`
	Action      string    `json:"action"`
	Host        string    `json:"host"`
	Participant string    `json:"participant"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	GracedTo    time.Time `json:"graced_to"`
	Invalid     bool      `json:"invalid,omitempty"`
}

// invariantEvaluators run in this order; each name prefixes its checks and keys its table in invariants.json.
var invariantEvaluators = []struct {
	name string
	fn   func(*invariantContext) ([]AssertionResult, any)
}{
	{"misses-only-in-fault-windows", evalMissesOnlyInFaultWindows},
	{"escrow-accounting-reconciles", evalEscrowAccountingReconciles},
	{"honest-hosts-agree-on-state", evalHonestHostsAgreeOnState},
	{"no-nonce-left-pending", evalNoNonceLeftPending},
	{"no-runtime-for-deactivated-escrow", evalNoRuntimeForDeactivatedEscrow},
	{"quarantines-match-faults", evalQuarantinesMatchFaults},
	{"error-rate-and-latency", evalErrorRateAndLatency},
	{"recovery-after-fault", evalRecoveryAfterFault},
	{"no-restarts-or-resource-growth", evalNoRestartsOrResourceGrowth},
	{"faults-fired-and-ml-nodes-bound", evalFaultsFiredAndMLNodesBound},
	{"no-skipped-validation-leases", evalNoSkippedValidationLeases},
	{"no-admission-during-poc", evalNoAdmissionDuringPoC},
	{"responses-compress", evalResponsesCompress},
	{"payload-files-are-zstd", evalPayloadFilesAreZstd},
	{"no-http-5xx-on-honest-hosts", evalNoHTTP5xxOnHonestHosts},
	{"no-escrow-falsely-marked-missing", evalNoEscrowFalselyMarkedMissing},
}

// evaluateInvariants runs the invariants and returns the checks and the report.
func evaluateInvariants(in InvariantInputs) ([]AssertionResult, InvariantReport) {
	c := newInvariantContext(in)
	report := InvariantReport{Grace: in.Settings.Grace.String(), Honest: c.honestList(), Tables: map[string]any{}}
	for _, w := range c.windows {
		report.Faulty = append(report.Faulty, faultWindowView{Event: w.Event.ID, Case: w.Event.Case, Action: w.Event.Action, Host: w.Host, Participant: w.Participant, From: w.Span.From, To: w.Span.To, GracedTo: w.Graced.To, Invalid: overlapsAny(w.Graced, c.invalid)})
	}
	var checks []AssertionResult
	for _, eval := range invariantEvaluators {
		results, table := eval.fn(c)
		checks = append(checks, results...)
		if table != nil {
			report.Tables[eval.name] = table
		}
	}
	report.Health = harnessHealth{Stalls: c.stalls, InvalidWindows: c.invalid, InvalidFraction: invalidFraction(c.invalid, in.Started, c.runEnd)}
	report.Health.InvalidChecks = markInvalid(checks, c.invalid)
	report.Results = checks
	return checks, report
}

func writeInvariantReport(outputDir string, report InvariantReport) error {
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, "invariants.json"), append(body, '\n'), 0o644)
}

// ---- misses-only-in-fault-windows: misses only charged to faulty hosts (GW-acct, Chain + Host-stats group) ----

type missDelta struct {
	Participant string    `json:"participant"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	FromLabel   string    `json:"from_label"`
	ToLabel     string    `json:"to_label"`
	Misses      uint64    `json:"protocol_misses"`
	HostMissed  uint64    `json:"host_missed"`
	Events      []string  `json:"events,omitempty"`
}

func accountingByParticipant(snapshot AccountingSnapshot) map[string][2]uint64 {
	out := map[string][2]uint64{}
	for _, record := range snapshot.Participants {
		totals := out[record.Participant]
		totals[0] += record.ProtocolMisses
		totals[1] += record.CrossChecks.HostMissed
		out[record.Participant] = totals
	}
	return out
}

func usableAccounting(snapshots []AccountingSnapshot) []AccountingSnapshot {
	var out []AccountingSnapshot
	for _, s := range snapshots {
		if s.Error == "" && len(s.Participants) > 0 {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// accountingDeltas diffs consecutive snapshots of one epoch; a change of
// epoch or a decreasing counter is a reset, not a delta.
func (c *invariantContext) accountingDeltas() ([]missDelta, int) {
	snapshots := usableAccounting(c.in.Accounting)
	var deltas []missDelta
	resets := 0
	for i := 1; i < len(snapshots); i++ {
		prev, next := snapshots[i-1], snapshots[i]
		if prev.EpochIndex != next.EpochIndex {
			resets++
			continue
		}
		before, after := accountingByParticipant(prev), accountingByParticipant(next)
		for participant, totals := range after {
			old := before[participant]
			if totals[0] < old[0] || totals[1] < old[1] {
				resets++
				continue
			}
			d := missDelta{Participant: participant, From: prev.At, To: next.At, FromLabel: prev.Label, ToLabel: next.Label, Misses: totals[0] - old[0], HostMissed: totals[1] - old[1]}
			if d.Misses == 0 && d.HostMissed == 0 {
				continue
			}
			for _, w := range c.faultyDuring(participant, prev.At, next.At) {
				d.Events = append(d.Events, w.Event.ID)
			}
			deltas = append(deltas, d)
		}
	}
	return deltas, resets
}

func waivesMisses(record EventRecord) bool {
	waive, _ := record.Params["waive_misses"].(bool)
	return waive || record.Action == "dapi_challenge"
}

func evalMissesOnlyInFaultWindows(c *invariantContext) ([]AssertionResult, any) {
	caseID := c.runCase("misses-only-in-fault-windows")
	snapshots := usableAccounting(c.in.Accounting)
	deltas, resets := c.accountingDeltas()
	table := map[string]any{"deltas": deltas, "epoch_or_counter_resets": resets, "snapshots": len(snapshots)}
	var checks []AssertionResult
	if len(snapshots) < 2 {
		checks = append(checks, invariantCheck("misses-only-in-fault-windows.honest_misses_gateway", caseID, false, ">= 2 accounting snapshots", fmt.Sprintf("%d", len(snapshots)), "GW-acct snapshots unavailable"))
	} else {
		var honestHits, unattributed []string
		var honestSpans, unattributedSpans []timeSpan
		for _, d := range deltas {
			if c.honest[d.Participant] {
				honestHits = append(honestHits, fmt.Sprintf("%s +%d/%d in %s..%s", d.Participant, d.Misses, d.HostMissed, d.FromLabel, d.ToLabel))
				honestSpans = append(honestSpans, timeSpan{From: d.From, To: d.To})
			} else if len(d.Events) == 0 {
				unattributed = append(unattributed, fmt.Sprintf("%s +%d/%d in %s..%s", d.Participant, d.Misses, d.HostMissed, d.FromLabel, d.ToLabel))
				unattributedSpans = append(unattributedSpans, timeSpan{From: d.From, To: d.To})
			}
		}
		checks = append(checks,
			withSpans(invariantCheck("misses-only-in-fault-windows.honest_misses_gateway", caseID, len(honestHits) == 0, "honest participants: delta protocol_misses == delta host_missed == 0", fmt.Sprintf("violations=%d", len(honestHits)), strings.Join(honestHits, "; ")), honestSpans...),
			withSpans(invariantCheck("misses-only-in-fault-windows.misses_attributed", caseID, len(unattributed) == 0, "every miss delta overlaps a fault window + grace of that participant", fmt.Sprintf("unattributed=%d", len(unattributed)), strings.Join(unattributed, "; ")), unattributedSpans...))
		for _, w := range c.windows {
			if !waivesMisses(w.Event) {
				continue
			}
			var charged uint64
			var chargedSpans []timeSpan
			for _, d := range deltas {
				if d.Participant != w.Participant || !w.Graced.overlapsAfter(d.From, d.To) {
					continue
				}
				if w.Background && c.inForegroundWindow(d.Participant, d.From, d.To) {
					continue
				}
				charged += d.Misses
				if d.Misses > 0 {
					chargedSpans = append(chargedSpans, timeSpan{From: d.From, To: d.To})
				}
			}
			spans := []timeSpan{w.Graced}
			if w.Background {
				spans = chargedSpans
			}
			checks = append(checks, withSpans(invariantCheck("misses-only-in-fault-windows."+w.Event.ID+".waived_misses", w.Event.Case, charged == 0, "0 misses charged inside a waived window", fmt.Sprintf("misses=%d", charged), w.Participant), spans...))
		}
	}
	chainHits, unmapped := c.chainHonestMisses()
	table["chain_honest_misses"], table["chain_unmapped_missed"] = chainHits, unmapped
	detail := strings.Join(chainHits, "; ")
	if unmapped > 0 {
		detail = strings.TrimPrefix(detail+fmt.Sprintf("; %d missed on slots without a Host-stats group mapping", unmapped), "; ")
	}
	checks = append(checks, invariantCheck("misses-only-in-fault-windows.honest_misses_chain", caseID, len(chainHits) == 0 && unmapped == 0, "settle host_stats: missed == 0 for honest slots", fmt.Sprintf("violations=%d unmapped=%d settles=%d", len(chainHits), unmapped, len(c.in.SettleTxs)), detail))
	return checks, table
}

// slotOwners maps escrow -> slot -> participant from Host-stats groups.
func (c *invariantContext) slotOwners() map[string]map[uint32]string {
	owners := map[string]map[uint32]string{}
	for _, s := range c.in.HostStats {
		if s.Kind != "shard" || len(s.Group) == 0 {
			continue
		}
		if owners[s.EscrowID] == nil {
			owners[s.EscrowID] = map[uint32]string{}
		}
		for _, slot := range s.Group {
			owners[s.EscrowID][slot.SlotID] = slot.ValidatorAddress
		}
	}
	return owners
}

func (c *invariantContext) chainHonestMisses() ([]string, int) {
	owners := c.slotOwners()
	var hits []string
	unmapped := 0
	for _, tx := range c.in.SettleTxs {
		if tx.Settle == nil {
			continue
		}
		escrow := strconv.FormatUint(tx.Settle.EscrowID, 10)
		for _, stat := range tx.Settle.HostStats {
			if stat.Missed == 0 {
				continue
			}
			owner, ok := owners[escrow][stat.SlotID]
			if !ok {
				unmapped++
				continue
			}
			if c.honest[owner] {
				hits = append(hits, fmt.Sprintf("escrow %s slot %d (%s) missed=%d", escrow, stat.SlotID, owner, stat.Missed))
			}
		}
	}
	return hits, unmapped
}

// ---- escrow-accounting-reconciles: escrow accounting reconciles (Chain, GW-dbg devshards and inferences) ----

type escrowReconciliation struct {
	EscrowID     string `json:"escrow_id"`
	Model        string `json:"model"`
	Active       bool   `json:"active"`
	Settlement   bool   `json:"settlement_enabled"`
	SettleTxs    int    `json:"settle_txs"`
	Expected     string `json:"expected_settles"`
	Amount       uint64 `json:"amount"`
	Cost         uint64 `json:"cost"`
	Fees         uint64 `json:"fees"`
	Refund       int64  `json:"refund"`
	SettledNonce uint64 `json:"settled_nonce,omitempty"`
	GatewayNonce uint64 `json:"gateway_nonce,omitempty"`
	// FinalNonce is the highest of the resident, settled and observed nonces (escrow-accounting-reconciles.final_nonce_le_max).
	FinalNonce    uint64 `json:"final_nonce"`
	InferenceCost *int64 `json:"inference_cost,omitempty"`
	// ByStatus breaks the gateway's inference records down per status, so a
	// cost mismatch can be traced to the statuses that carry it.
	ByStatus map[string]*statusCost `json:"by_status,omitempty"`
}

type statusCost struct {
	Count    int    `json:"count"`
	Actual   uint64 `json:"actual_cost"`
	Reserved uint64 `json:"reserved_cost"`
}

func evalEscrowAccountingReconciles(c *invariantContext) ([]AssertionResult, any) {
	caseID := c.runCase("escrow-accounting-reconciles")
	if c.in.ChainError != "" || c.in.Devshards == nil {
		detail := c.in.ChainError
		if c.in.Devshards == nil {
			detail = strings.TrimPrefix(detail+"; gateway devshard list unavailable", "; ")
		}
		return []AssertionResult{invariantCheck("escrow-accounting-reconciles.settle_count", caseID, false, "chain ledger and gateway devshards available", "unavailable", detail)}, nil
	}
	settles := map[string][]*ledgerface.SettleTxView{}
	for _, tx := range c.in.SettleTxs {
		if tx.Settle != nil {
			id := strconv.FormatUint(tx.Settle.EscrowID, 10)
			settles[id] = append(settles[id], tx.Settle)
		}
	}
	escrows := map[string]ledgerface.EscrowView{}
	for _, e := range c.in.Escrows {
		escrows[strconv.FormatUint(e.ID, 10)] = e
	}
	observedNonce := map[string]uint64{}
	for _, s := range c.in.Pending {
		if s.EscrowID != "" && s.Error == "" {
			observedNonce[s.EscrowID] = max(observedNonce[s.EscrowID], s.Nonce)
		}
	}
	var rows []escrowReconciliation
	var countBad, balanceBad, nonceBad, costBad, overMax []string
	for _, d := range c.in.Devshards {
		txs := settles[d.ID]
		row := escrowReconciliation{EscrowID: d.ID, Model: d.Model, Active: d.Active, Settlement: !c.in.SettlementDisabled[d.Model], SettleTxs: len(txs)}
		row.FinalNonce = observedNonce[d.ID]
		if d.Resident {
			row.FinalNonce = max(row.FinalNonce, d.Nonce)
		}
		if len(txs) > 0 {
			row.FinalNonce = max(row.FinalNonce, txs[len(txs)-1].Nonce)
		}
		if c.in.ChainMaxNonce > 0 && row.FinalNonce > uint64(c.in.ChainMaxNonce) {
			overMax = append(overMax, fmt.Sprintf("%s final nonce %d > max_nonce %d", d.ID, row.FinalNonce, c.in.ChainMaxNonce))
		}
		switch {
		case !row.Settlement:
			row.Expected = "0"
			if len(txs) != 0 {
				countBad = append(countBad, fmt.Sprintf("%s (settlement off) settled %d times", d.ID, len(txs)))
			}
		case d.Active:
			row.Expected = "<=1 (still active)"
			if len(txs) > 1 {
				countBad = append(countBad, fmt.Sprintf("%s settled %d times", d.ID, len(txs)))
			}
		default:
			row.Expected = "1"
			if len(txs) != 1 {
				countBad = append(countBad, fmt.Sprintf("%s deactivated, settled %d times", d.ID, len(txs)))
			}
		}
		if len(txs) > 0 {
			last := txs[len(txs)-1]
			for _, stat := range last.HostStats {
				row.Cost += stat.Cost
			}
			row.Fees, row.SettledNonce = last.Fees, last.Nonce
			if e, ok := escrows[d.ID]; ok {
				row.Amount = e.Amount
				row.Refund = int64(e.Amount) - int64(row.Cost) - int64(row.Fees)
				if row.Refund < 0 {
					balanceBad = append(balanceBad, fmt.Sprintf("%s cost %d + fees %d > amount %d", d.ID, row.Cost, row.Fees, e.Amount))
				}
			} else {
				balanceBad = append(balanceBad, d.ID+" missing from the mock-chain escrow table")
			}
			if d.Resident {
				row.GatewayNonce = d.Nonce
				if last.Nonce != d.Nonce {
					nonceBad = append(nonceBad, fmt.Sprintf("%s settled nonce %d != gateway nonce %d", d.ID, last.Nonce, d.Nonce))
				}
			} else if observed := observedNonce[d.ID]; observed > last.Nonce {
				row.GatewayNonce = observed
				nonceBad = append(nonceBad, fmt.Sprintf("%s settled nonce %d < observed gateway nonce %d", d.ID, last.Nonce, observed))
			}
			if inferences, ok := c.in.Inferences[d.ID]; ok {
				var sum int64
				row.ByStatus = map[string]*statusCost{}
				for _, inference := range inferences {
					sc := row.ByStatus[inference.Status]
					if sc == nil {
						sc = &statusCost{}
						row.ByStatus[inference.Status] = sc
					}
					sc.Count++
					sc.Actual += inference.ActualCost
					sc.Reserved += inference.ReservedCost
					// A challenged record keeps its cost in host_stats until the vote
					// resolves, and settlement seals it as it stands.
					switch inference.Status {
					case "finished", "validated", "challenged":
						sum += int64(inference.ActualCost)
					}
				}
				row.InferenceCost = &sum
				if sum != int64(row.Cost) {
					costBad = append(costBad, fmt.Sprintf("%s settle cost %d != finished/validated/challenged inference cost %d", d.ID, row.Cost, sum))
				}
			}
		}
		rows = append(rows, row)
	}
	for id, txs := range settles {
		if !containsDevshard(c.in.Devshards, id) && len(txs) > 1 {
			countBad = append(countBad, fmt.Sprintf("%s (unknown to gateway) settled %d times", id, len(txs)))
		}
	}
	checks := []AssertionResult{
		invariantCheck("escrow-accounting-reconciles.settle_count", caseID, len(countBad) == 0, "exactly one settle per deactivated escrow with settlement on; none with it off; never two", fmt.Sprintf("violations=%d escrows=%d", len(countBad), len(rows)), strings.Join(countBad, "; ")),
		invariantCheck("escrow-accounting-reconciles.balance", caseID, len(balanceBad) == 0, "sum(host_stats.cost) + fees <= amount", fmt.Sprintf("violations=%d", len(balanceBad)), strings.Join(balanceBad, "; ")),
		invariantCheck("escrow-accounting-reconciles.settled_nonce", caseID, len(nonceBad) == 0, "settled nonce == gateway final nonce", fmt.Sprintf("violations=%d", len(nonceBad)), strings.Join(nonceBad, "; ")),
		invariantCheck("escrow-accounting-reconciles.cost_matches_inferences", caseID, len(costBad) == 0, "sum(host_stats.cost) == sum(actual_cost of finished/validated/challenged inferences)", fmt.Sprintf("violations=%d", len(costBad)), strings.Join(costBad, "; ")),
	}
	if c.in.ChainMaxNonce > 0 {
		checks = append(checks, invariantCheck("escrow-accounting-reconciles.final_nonce_le_max", c.checkCase("escrow-accounting-reconciles", "final_nonce_le_max"), len(overMax) == 0,
			fmt.Sprintf("every escrow's final gateway session nonce <= chain max_nonce %d", c.in.ChainMaxNonce), fmt.Sprintf("violations=%d escrows=%d", len(overMax), len(rows)), strings.Join(overMax, "; ")))
	}
	return checks, rows
}

func containsDevshard(devshards []GatewayDevshard, id string) bool {
	for _, d := range devshards {
		if d.ID == id {
			return true
		}
	}
	return false
}

// ---- honest-hosts-agree-on-state: honest hosts agree on state roots (Host-stats, Chain, GW-prom fork counter) ----

func evalHonestHostsAgreeOnState(c *invariantContext) ([]AssertionResult, any) {
	caseID := c.runCase("honest-hosts-agree-on-state")
	type key struct {
		escrow string
		nonce  uint64
	}
	roots := map[key]map[string][]string{}
	for _, s := range c.in.HostStats {
		if s.Kind != "shard" || s.Error != "" || s.StateRoot == "" {
			continue
		}
		if !c.honest[s.Participant] && c.recentlyFaulty(s.Participant, s.At) {
			continue
		}
		k := key{s.EscrowID, s.Nonce}
		if roots[k] == nil {
			roots[k] = map[string][]string{}
		}
		roots[k][s.StateRoot] = appendUnique(roots[k][s.StateRoot], s.Host)
	}
	var conflicts []string
	compared := 0
	for k, byRoot := range roots {
		hosts := 0
		for _, h := range byRoot {
			hosts += len(h)
		}
		if hosts >= 2 {
			compared++
		}
		if len(byRoot) > 1 {
			parts := make([]string, 0, len(byRoot))
			for root, h := range byRoot {
				parts = append(parts, fmt.Sprintf("%s=%s", strings.Join(h, ","), root))
			}
			sort.Strings(parts)
			conflicts = append(conflicts, fmt.Sprintf("escrow %s nonce %d: %s", k.escrow, k.nonce, strings.Join(parts, " vs ")))
		}
	}
	sort.Strings(conflicts)
	var settleBad []string
	settleCompared := 0
	for _, tx := range c.in.SettleTxs {
		if tx.Settle == nil {
			continue
		}
		byRoot := roots[key{strconv.FormatUint(tx.Settle.EscrowID, 10), tx.Settle.Nonce}]
		if len(byRoot) == 0 {
			continue
		}
		settleCompared++
		if _, ok := byRoot[tx.Settle.StateRootAndProtocolVersion]; !ok || len(byRoot) > 1 {
			settleBad = append(settleBad, fmt.Sprintf("escrow %d nonce %d settle root %s", tx.Settle.EscrowID, tx.Settle.Nonce, tx.Settle.StateRootAndProtocolVersion))
		}
	}
	forks := c.forkDeltas()
	var forkHits []string
	for target, delta := range forks {
		if delta > 0 {
			forkHits = append(forkHits, fmt.Sprintf("%s +%.0f", target, delta))
		}
	}
	sort.Strings(forkHits)
	rootCheck := invariantCheck("honest-hosts-agree-on-state.state_roots_agree", caseID, len(conflicts) == 0 && compared > 0, "same escrow and nonce => same root on honest and recovered hosts", fmt.Sprintf("conflicts=%d compared_groups=%d", len(conflicts), compared), strings.Join(conflicts, "; "))
	if compared == 0 && len(conflicts) == 0 {
		rootCheck.Details = "no escrow nonce was observed on two hosts"
	}
	return []AssertionResult{
		rootCheck,
		invariantCheck("honest-hosts-agree-on-state.settle_root_matches", caseID, len(settleBad) == 0, "settle state_root_and_protocol_version == honest host root at that nonce", fmt.Sprintf("mismatches=%d compared=%d", len(settleBad), settleCompared), strings.Join(settleBad, "; ")),
		invariantCheck("honest-hosts-agree-on-state.no_fork_detected", caseID, len(forkHits) == 0, "devshard_diff_fork_detected_total delta == 0", fmt.Sprintf("hosts_with_forks=%d", len(forkHits)), strings.Join(forkHits, "; ")),
	}, map[string]any{"conflicts": conflicts, "compared_groups": compared, "fork_deltas": forks}
}

// recentlyFaulty excludes a faulty host's sample until it has had
// stateRootRecovery to catch up after its window and grace.
func (c *invariantContext) recentlyFaulty(participant string, at time.Time) bool {
	for _, w := range c.windows {
		if w.Participant == participant && !w.Background && !at.Before(w.Span.From) && !at.After(w.Graced.To.Add(stateRootRecovery)) {
			return true
		}
	}
	return false
}

func appendUnique(values []string, value string) []string {
	for _, v := range values {
		if v == value {
			return values
		}
	}
	return append(values, value)
}

// domainSum adds every Domain series of one family in a sample.
func domainSum(sample MetricsSample, family string) (float64, bool) {
	total, found := 0.0, false
	for key, value := range sample.Domain {
		if strings.HasPrefix(key, family+"{") {
			total += value
			found = true
		}
	}
	return total, found
}

// forkDeltas sums increases per target; a decrease is a process restart.
func (c *invariantContext) forkDeltas() map[string]float64 {
	out := map[string]float64{}
	last := map[string]float64{}
	for _, s := range sortedMetrics(c.in.Metrics) {
		value, ok := domainSum(s, "devshard_diff_fork_detected_total")
		if !ok {
			continue
		}
		if prev, seen := last[s.Target]; seen {
			out[s.Target] += counterIncrease(prev, value)
		} else {
			out[s.Target] += 0
		}
		last[s.Target] = value
	}
	return out
}

func counterIncrease(prev, value float64) float64 {
	if value < prev {
		return value
	}
	return value - prev
}

func sortedMetrics(samples []MetricsSample) []MetricsSample {
	out := append([]MetricsSample(nil), samples...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// ---- no-nonce-left-pending: no nonce stays Pending forever (GW-dbg pending and inferences) ----

func evalNoNonceLeftPending(c *invariantContext) ([]AssertionResult, any) {
	caseID := c.runCase("no-nonce-left-pending")
	var usable []PendingSample
	for _, s := range c.in.Pending {
		if s.Error == "" && s.EscrowID != "" {
			usable = append(usable, s)
		}
	}
	if len(usable) == 0 {
		return []AssertionResult{invariantCheck("no-nonce-left-pending.max_pending_age", caseID, false, "pending samples available", "none", "GW-dbg pending poller produced no usable sample")}, nil
	}
	limit := c.in.Settings.MaxPendingAge.Seconds()
	burned := burnedNonces(c.in.Burns)
	var worst PendingSample
	var over []string
	var overSpans []timeSpan
	var burnedMax float64
	burnedOver, legacy := 0, 0
	latest := usable[0].At
	for _, s := range usable {
		if s.Open > 0 && s.OpenStarted == nil {
			legacy++
		}
		s, burnedAge := withoutBurned(s, burned[s.EscrowID])
		burnedMax = max(burnedMax, burnedAge)
		if burnedAge > limit {
			burnedOver++
		}
		if s.MaxAgeSeconds > worst.MaxAgeSeconds {
			worst = s
		}
		if s.MaxAgeSeconds > limit {
			over = append(over, fmt.Sprintf("%s escrow %s inference %s (%s) age %.0fs", s.At.Format(time.RFC3339), s.EscrowID, s.OldestInference, s.OldestStatus, s.MaxAgeSeconds))
			overSpans = append(overSpans, timeSpan{From: s.At.Add(-time.Duration(s.MaxAgeSeconds * float64(time.Second))), To: s.At})
		}
		if s.At.After(latest) {
			latest = s.At
		}
	}
	// One final sample per escrow: the poller can land twice inside the window.
	final := map[string]PendingSample{}
	for _, s := range usable {
		if latest.Sub(s.At) <= pendingPollEvery/2 && s.At.After(final[s.EscrowID].At) {
			final[s.EscrowID] = s
		}
	}
	// A burned nonce stays open until its escrow closes, and an escrow created during the drain holds
	// only its warm-up probe, whose finish waits for the next request's diff; both are reported apart.
	var dirty, apart []string
	for _, s := range final {
		open := s.Open
		for id := range s.OpenStarted {
			if burned[s.EscrowID][id] {
				open--
			}
		}
		switch {
		case open == 0 && s.PendingTxs == 0:
			if s.Open > 0 {
				apart = append(apart, fmt.Sprintf("escrow %s burned_open=%d", s.EscrowID, s.Open))
			}
		case s.Nonce <= 1:
			apart = append(apart, fmt.Sprintf("escrow %s warm-up only, nonce=%d open=%d pending_txs=%d", s.EscrowID, s.Nonce, open, s.PendingTxs))
		default:
			dirty = append(dirty, fmt.Sprintf("escrow %s open=%d pending_txs=%d", s.EscrowID, open, s.PendingTxs))
		}
	}
	sort.Strings(dirty)
	sort.Strings(apart)
	return []AssertionResult{
		withSpans(invariantCheck("no-nonce-left-pending.max_pending_age", caseID, len(over) == 0, fmt.Sprintf("max pending/started age <= %s at every sample, burned nonces apart", c.in.Settings.MaxPendingAge), fmt.Sprintf("max=%.0fs samples_over=%d burned_max=%.0fs burned_samples_over=%d", worst.MaxAgeSeconds, len(over), burnedMax, burnedOver), strings.Join(firstN(over, 10), "; ")), overSpans...),
		invariantCheck("no-nonce-left-pending.clean_end_state", caseID, len(dirty) == 0, "final poll: no open inference and no pending tx, burned nonces and warm-up-only escrows apart", fmt.Sprintf("dirty=%d apart=%d", len(dirty), len(apart)), strings.Join(append(dirty, apart...), "; ")),
	}, pendingTable(worst, limit, len(over), burnedMax, burnedOver, legacy)
}

// withoutBurned returns the sample with its oldest open nonce recomputed over the nonces the gateway
// did not burn, and the oldest burned age. A burned nonce is never sent, so no timeout is voted on
// it and it stays pending until the escrow closes. A sample without per-nonce start times is kept.
func withoutBurned(s PendingSample, burned map[string]bool) (PendingSample, float64) {
	if s.OpenStarted == nil || len(burned) == 0 {
		return s, 0
	}
	var burnedAge float64
	original := s
	s.MaxAgeSeconds, s.OldestInference, s.OldestStatus = 0, "", ""
	for id, started := range s.OpenStarted {
		age := s.At.Sub(time.Unix(started, 0)).Seconds()
		switch {
		case burned[id]:
			burnedAge = max(burnedAge, age)
		case age > s.MaxAgeSeconds:
			s.MaxAgeSeconds, s.OldestInference = age, id
		}
	}
	if s.OldestInference == original.OldestInference {
		s.OldestStatus = original.OldestStatus
	} else if s.OldestInference != "" {
		s.OldestStatus = "open"
	}
	return s, burnedAge
}

func pendingTable(worst PendingSample, limit float64, over int, burnedMax float64, burnedOver, legacy int) map[string]any {
	table := map[string]any{"max_age_seconds": worst.MaxAgeSeconds, "max_age_escrow": worst.EscrowID, "limit_seconds": limit, "over_limit": over,
		"burned_max_age_seconds": burnedMax, "burned_samples_over_limit": burnedOver}
	if legacy > 0 {
		table["note"] = fmt.Sprintf("%d samples with open work carry no per-nonce start times, so burned nonces in them count", legacy)
	}
	return table
}

func firstN(values []string, n int) []string {
	if len(values) > n {
		return append(values[:n:n], fmt.Sprintf("... %d more", len(values)-n))
	}
	return values
}

// ---- no-runtime-for-deactivated-escrow: no resident runtime for a deactivated escrow (GW-dbg /v1/admin/devshards) ----

func evalNoRuntimeForDeactivatedEscrow(c *invariantContext) ([]AssertionResult, any) {
	caseID := c.runCase("no-runtime-for-deactivated-escrow")
	if c.in.Devshards == nil {
		return []AssertionResult{invariantCheck("no-runtime-for-deactivated-escrow.no_resident_deactivated", caseID, false, "gateway devshard list available", "unavailable", "")}, nil
	}
	var resident []string
	for _, d := range c.in.Devshards {
		if !d.Active && d.Resident {
			resident = append(resident, fmt.Sprintf("%s (%s)", d.ID, d.Model))
		}
	}
	return []AssertionResult{
		invariantCheck("no-runtime-for-deactivated-escrow.no_resident_deactivated", caseID, len(resident) == 0, "no runtime in the gateway registry for a deactivated escrow", fmt.Sprintf("resident_deactivated=%d", len(resident)), strings.Join(resident, "; ")),
	}, map[string]any{"resident_deactivated": resident}
}

// ---- quarantines-match-faults: quarantine durations proportional to cause (GW-prom transitions, GW-dbg throttle) ----

type quarantineTransition struct {
	At          time.Time `json:"at"`
	Participant string    `json:"participant"`
	Mode        string    `json:"mode"`
	Reason      string    `json:"reason"`
	Count       float64   `json:"count"`
	Event       string    `json:"event,omitempty"`
}

type quarantineEpisode struct {
	Participant string    `json:"participant"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	Until       time.Time `json:"until,omitempty"`
	// Longest is the longest single quarantine in the episode: a quarantine_until less the poll
	// that first showed it. A quarantine renewed as it ends starts a new one in the same episode.
	Longest time.Duration `json:"longest,omitempty"`
	Event   string        `json:"event,omitempty"`
}

const quarantineFamily = "devshard_gateway_participant_quarantine_transitions_total"

// parseDomainKey splits name{a="x",b="y"} into its labels.
func parseDomainKey(key string) (string, map[string]string) {
	name, rest, found := strings.Cut(key, "{")
	labels := map[string]string{}
	if !found {
		return name, labels
	}
	rest = strings.TrimSuffix(rest, "}")
	for rest != "" {
		label, after, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		value, remaining, err := unquotePrefix(after)
		if err != nil {
			break
		}
		labels[label] = value
		rest = strings.TrimPrefix(remaining, ",")
	}
	return name, labels
}

func unquotePrefix(s string) (string, string, error) {
	prefix, err := strconv.QuotedPrefix(s)
	if err != nil {
		return "", "", err
	}
	value, err := strconv.Unquote(prefix)
	return value, s[len(prefix):], err
}

// quarantineTransitions turns transition counters into increments; the
// probation step after a quarantine expires is not a new quarantine.
func (c *invariantContext) quarantineTransitions() []quarantineTransition {
	var out []quarantineTransition
	previous := map[string]float64{}
	for _, s := range sortedMetrics(c.in.Metrics) {
		if s.Target != "gateway" {
			continue
		}
		for key, value := range s.Domain {
			name, labels := parseDomainKey(key)
			if name != quarantineFamily {
				continue
			}
			prev, seen := previous[key]
			previous[key] = value
			increase := value
			if seen {
				increase = counterIncrease(prev, value)
			}
			if increase <= 0 || labels["reason"] == "quarantine_expired" || labels["mode"] == "probation" {
				continue
			}
			t := quarantineTransition{At: s.At, Participant: labels["participant_key"], Mode: labels["mode"], Reason: labels["reason"], Count: increase}
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

func quarantineEpisodes(samples []ThrottleSample) []quarantineEpisode {
	type point struct {
		at    time.Time
		in    bool
		until time.Time
	}
	series := map[string][]point{}
	for _, s := range samples {
		for _, p := range s.Participants {
			until, _ := time.Parse(time.RFC3339Nano, p.QuarantineUntil)
			series[p.ParticipantKey] = append(series[p.ParticipantKey], point{at: s.At, in: p.inQuarantine(), until: until})
		}
	}
	var episodes []quarantineEpisode
	for participant, points := range series {
		sort.SliceStable(points, func(i, j int) bool { return points[i].at.Before(points[j].at) })
		var current *quarantineEpisode
		firstShown := map[time.Time]time.Time{}
		for _, p := range points {
			switch {
			case p.in && current != nil && p.at.Sub(current.To) <= 3*throttlePollEvery:
				current.To = p.at
				if p.until.After(current.Until) {
					current.Until = p.until
				}
			case p.in:
				if current != nil {
					episodes = append(episodes, *current)
				}
				current = &quarantineEpisode{Participant: participant, From: p.at, To: p.at, Until: p.until}
			case current != nil && !p.at.Equal(current.To):
				episodes = append(episodes, *current)
				current = nil
			}
			if p.in && !p.until.IsZero() {
				if _, seen := firstShown[p.until]; !seen {
					firstShown[p.until] = p.at
				}
				current.Longest = max(current.Longest, p.until.Sub(firstShown[p.until]))
			}
		}
		if current != nil {
			episodes = append(episodes, *current)
		}
	}
	sort.SliceStable(episodes, func(i, j int) bool { return episodes[i].From.Before(episodes[j].From) })
	return episodes
}

// quarantineReasonFamily maps a gateway reason label to the setting that
// sets its duration.
func quarantineReasonFamily(reason string) string {
	switch {
	case strings.HasPrefix(reason, "sender_rate"):
		return "sender_rate"
	case strings.HasPrefix(reason, "http_"):
		return "http"
	case strings.HasPrefix(reason, "empty_stream"):
		return "empty_stream"
	case strings.HasPrefix(reason, "stalled_winner"):
		return "stalled_winner"
	case strings.Contains(reason, "transport"), strings.HasPrefix(reason, "escrow_lookup"):
		return "transport"
	}
	return ""
}

// expectedQuarantine returns the reason families an event may cause, and
// whether the event states an expectation at all. params.expect_quarantine
// ("none" or a comma list of families) overrides the action default.
func expectedQuarantine(record EventRecord) (allowed map[string]bool, forbidden map[string]bool, specified bool) {
	if raw, ok := record.Params["expect_quarantine"].(string); ok {
		allowed = map[string]bool{}
		if raw != "none" {
			for _, family := range strings.Split(raw, ",") {
				allowed[strings.TrimSpace(family)] = true
			}
		}
		return allowed, nil, true
	}
	switch record.Action {
	case "ml_fault":
		patch, _ := record.Params["patch"].(map[string]any)
		switch {
		case patch["empty_stream"] == true:
			return map[string]bool{"empty_stream": true}, nil, true
		case patch["http_status"] != nil:
			return map[string]bool{"http": true}, nil, true
		case patch["pause_stream"] == true, patch["hang"] == true:
			// The host has sent its receipt but no content when the gateway gives up on the
			// attempt, which it reports as an empty stream.
			return map[string]bool{"stalled_winner": true, "transport": true, "empty_stream": true}, nil, true
		}
	case "dapi_challenge":
		return nil, map[string]bool{"empty_stream": true}, true
	}
	return nil, nil, false
}

func evalQuarantinesMatchFaults(c *invariantContext) ([]AssertionResult, any) {
	caseID := c.runCase("quarantines-match-faults")
	transitions := c.quarantineTransitions()
	episodes := quarantineEpisodes(c.in.Throttle)
	var honestHits, unattributed []string
	var honestSpans, unattributedSpans []timeSpan
	eventBad := map[string][]string{}
	eventBadSpans := map[string][]timeSpan{}
	for i := range transitions {
		t := &transitions[i]
		windows := c.faultyDuring(t.Participant, t.At.Add(-hostStatsPollEvery), t.At)
		if c.honest[t.Participant] {
			honestHits = append(honestHits, fmt.Sprintf("%s %s/%s at %s", t.Participant, t.Mode, t.Reason, t.At.Format(time.RFC3339)))
			honestSpans = append(honestSpans, timeSpan{From: t.At, To: t.At})
			continue
		}
		if len(windows) == 0 {
			unattributed = append(unattributed, fmt.Sprintf("%s %s/%s at %s", t.Participant, t.Mode, t.Reason, t.At.Format(time.RFC3339)))
			unattributedSpans = append(unattributedSpans, timeSpan{From: t.At, To: t.At})
			continue
		}
		t.Event = windows[0].Event.ID
		foreground := c.inForegroundWindow(t.Participant, t.At.Add(-hostStatsPollEvery), t.At)
		for _, w := range windows {
			if w.Background && foreground {
				continue
			}
			allowed, forbidden, specified := expectedQuarantine(w.Event)
			family := quarantineReasonFamily(t.Reason)
			if specified && (forbidden[family] || (allowed != nil && !allowed[family])) {
				eventBad[w.Event.ID] = append(eventBad[w.Event.ID], fmt.Sprintf("%s/%s", t.Mode, t.Reason))
				eventBadSpans[w.Event.ID] = append(eventBadSpans[w.Event.ID], timeSpan{From: t.At, To: t.At})
			}
		}
	}
	for i := range episodes {
		e := &episodes[i]
		if c.honest[e.Participant] {
			honestHits = append(honestHits, fmt.Sprintf("%s quarantined %s..%s", e.Participant, e.From.Format(time.RFC3339), e.To.Format(time.RFC3339)))
			honestSpans = append(honestSpans, timeSpan{From: e.From, To: e.To})
		}
		if windows := c.faultyDuring(e.Participant, e.From.Add(-c.in.Settings.Grace), e.From); len(windows) > 0 {
			e.Event = windows[0].Event.ID
		}
	}
	checks := []AssertionResult{
		withSpans(invariantCheck("quarantines-match-faults.honest_not_quarantined", caseID, len(honestHits) == 0, "no quarantine transition or throttle quarantine for honest participants", fmt.Sprintf("violations=%d", len(honestHits)), strings.Join(firstN(honestHits, 10), "; ")), honestSpans...),
		withSpans(invariantCheck("quarantines-match-faults.quarantine_attributed", caseID, len(unattributed) == 0, "every quarantine transition falls in a fault window of that participant", fmt.Sprintf("unattributed=%d", len(unattributed)), strings.Join(firstN(unattributed, 10), "; ")), unattributedSpans...),
	}
	for _, w := range c.windows {
		if allowed, forbidden, specified := expectedQuarantine(w.Event); specified {
			bad, spans := eventBad[w.Event.ID], []timeSpan{w.Graced}
			if w.Background {
				spans = eventBadSpans[w.Event.ID]
			}
			checks = append(checks, withSpans(invariantCheck("quarantines-match-faults."+w.Event.ID+".reason", w.Event.Case, len(bad) == 0, describeExpectation(allowed, forbidden), fmt.Sprintf("mismatched=%d", len(bad)), strings.Join(bad, "; ")), spans...))
		}
	}
	durationBad, durationNote := c.quarantineDurations(episodes, transitions)
	checks = append(checks, invariantCheck("quarantines-match-faults.durations", caseID, len(durationBad) == 0, "quarantine_until - start <= configured duration + 1 tick; no quarantine outlives its cause by more than that", fmt.Sprintf("violations=%d", len(durationBad)), strings.TrimPrefix(strings.Join(durationBad, "; ")+durationNote, "; ")))
	return checks, map[string]any{"transitions": transitions, "episodes": episodes}
}

func describeExpectation(allowed, forbidden map[string]bool) string {
	switch {
	case len(forbidden) > 0:
		return "no quarantine with reason family " + strings.Join(sortedKeys(forbidden), ",")
	case len(allowed) == 0:
		return "no quarantine"
	}
	return "quarantine reason family in " + strings.Join(sortedKeys(allowed), ",")
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (c *invariantContext) quarantineDurations(episodes []quarantineEpisode, transitions []quarantineTransition) ([]string, string) {
	var bad []string
	skipped := 0
	for _, e := range episodes {
		if c.honest[e.Participant] {
			continue
		}
		duration, ok := c.episodeDuration(e, transitions)
		if !ok {
			skipped++
			continue
		}
		if e.Longest > duration+quarantineDurationT {
			bad = append(bad, fmt.Sprintf("%s single quarantine %s > %s", e.Participant, e.Longest.Round(time.Second), duration))
		}
		// The episode is charged to the latest fault that can have caused it, so a quarantine
		// that starts in one fault is not blamed on an earlier fault whose grace it also falls in.
		var cause *faultWindow
		for _, w := range c.faultyDuring(e.Participant, e.From.Add(-c.in.Settings.Grace), e.From) {
			if cause == nil || w.Graced.To.After(cause.Graced.To) {
				cause = &w
			}
		}
		if cause != nil && e.To.After(cause.Graced.To.Add(duration+quarantineDurationT)) {
			bad = append(bad, fmt.Sprintf("%s quarantined until %s, cause %s ended %s", e.Participant, e.To.Format(time.RFC3339), cause.Event.ID, cause.Span.To.Format(time.RFC3339)))
		}
	}
	if skipped > 0 {
		return bad, fmt.Sprintf("; %d episodes skipped: reason duration not configured", skipped)
	}
	return bad, ""
}

// episodeDuration picks the configured duration of the transition closest
// before the episode start.
func (c *invariantContext) episodeDuration(e quarantineEpisode, transitions []quarantineTransition) (time.Duration, bool) {
	var reason string
	for _, t := range transitions {
		if t.Participant == e.Participant && !t.At.After(e.From.Add(throttlePollEvery)) {
			reason = t.Reason
		}
	}
	d, ok := c.in.Settings.Quarantine[quarantineReasonFamily(reason)]
	return d, ok
}

// ---- error-rate-and-latency: latency and error-rate bounds per window (Requests, Events) ----

// requestWindowStats counts the requests sent to the gateway; Dropped are the
// ones the generator never sent, kept out of Requests and the error rate.
type requestWindowStats struct {
	Window       string        `json:"window"`
	Requests     int           `json:"requests"`
	Dropped      int           `json:"dropped_by_generator"`
	Errors       int           `json:"errors"`
	ErrorRate    float64       `json:"error_rate"`
	P50          time.Duration `json:"p50"`
	P99          time.Duration `json:"p99"`
	EmptyStreams int           `json:"empty_streams"`
	NonRetryable int           `json:"errors_not_503_429"`
}

func windowStats(name string, requests []RequestResult) requestWindowStats {
	stats := requestWindowStats{Window: name}
	var latencies []time.Duration
	for _, r := range requests {
		if r.Outcome == "dropped_by_generator" {
			stats.Dropped++
			continue
		}
		stats.Requests++
		if r.Outcome == "error" {
			stats.Errors++
			if r.Status != 429 && r.Status != 503 {
				stats.NonRetryable++
			}
		} else {
			latencies = append(latencies, r.Duration)
		}
		if r.StreamOutcome == streamEmpty {
			stats.EmptyStreams++
		}
	}
	if stats.Requests > 0 {
		stats.ErrorRate = float64(stats.Errors) / float64(stats.Requests)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	stats.P50, stats.P99 = percentile(latencies, 0.50), percentile(latencies, 0.99)
	return stats
}

// eventErrorBound is params.max_error_rate, or 100% for a gateway restart
// (recovery-after-fault judges its recovery), else the racing bound of 2%.
func eventErrorBound(record EventRecord) float64 {
	if bound, ok := numberParam(record.Params, "max_error_rate"); ok {
		return bound
	}
	if record.Action == "gateway_restart" || record.Action == "epoch_advance" {
		return 1
	}
	return faultErrorRate
}

// activeEnd is when record stops failing requests. A gateway restart is up at
// EndedAt but may not serve yet, so it lasts to the first request start after
// EndedAt that succeeded, capped at restartRecoveryLim.
func (c *invariantContext) activeEnd(record EventRecord) time.Time {
	if record.EndedAt == nil {
		return c.runEnd
	}
	end := *record.EndedAt
	if record.Action != "gateway_restart" {
		return end
	}
	serving := end.Add(restartRecoveryLim)
	for _, r := range c.in.Requests {
		if r.Outcome != "error" && r.Outcome != "dropped_by_generator" && !r.StartedAt.Before(end) && r.StartedAt.Before(serving) {
			serving = r.StartedAt
		}
	}
	return serving
}

// allEventSpans covers every fired event, global ones included, with grace.
func (c *invariantContext) allEventSpans() []struct {
	record EventRecord
	span   timeSpan
} {
	var out []struct {
		record EventRecord
		span   timeSpan
	}
	for _, record := range c.in.Events {
		// A background marker is the whole run; the run-level bounds judge it.
		if record.StartedAt == nil || isBackground(record.Params) {
			continue
		}
		end := c.runEnd
		if record.EndedAt != nil {
			end = *record.EndedAt
		}
		out = append(out, struct {
			record EventRecord
			span   timeSpan
		}{record, timeSpan{From: *record.StartedAt, To: end.Add(c.in.Settings.Grace)}})
	}
	return out
}

func evalErrorRateAndLatency(c *invariantContext) ([]AssertionResult, any) {
	caseID := c.runCase("error-rate-and-latency")
	baselineSpan := timeSpan{From: c.in.Started.Add(c.in.Settings.Baseline[0]), To: c.in.Started.Add(c.in.Settings.Baseline[1])}
	spans := c.allEventSpans()
	activeEnds := make([]time.Time, len(spans))
	for i, s := range spans {
		activeEnds[i] = c.activeEnd(s.record)
	}
	var baseline, quiet []RequestResult
	// A request belongs to the faults active while it ran, else to the graced
	// tails its start falls in. One fault judges it against that fault's
	// bound; several report it under their overlap, judged by none.
	perEvent := map[string][]RequestResult{}
	perOverlap := map[string][]RequestResult{}
	overlapsByEvent := map[string][]string{}
	excluded := 0
	for _, r := range c.in.Requests {
		if overlapsAny(timeSpan{From: r.StartedAt, To: r.StartedAt}, c.invalid) {
			excluded++
			continue
		}
		var events, tails []string
		for i, s := range spans {
			switch {
			case !r.StartedAt.Add(r.Duration).Before(s.span.From) && !r.StartedAt.After(activeEnds[i]):
				events = append(events, s.record.ID)
			case s.span.contains(r.StartedAt):
				tails = append(tails, s.record.ID)
			}
		}
		if len(events) == 0 {
			events = tails
		}
		switch len(events) {
		case 0:
		case 1:
			perEvent[events[0]] = append(perEvent[events[0]], r)
		default:
			overlap := strings.Join(events, "+")
			if _, seen := perOverlap[overlap]; !seen {
				for _, event := range events {
					overlapsByEvent[event] = append(overlapsByEvent[event], overlap)
				}
			}
			perOverlap[overlap] = append(perOverlap[overlap], r)
		}
		switch {
		case len(events) > 0:
		case baselineSpan.contains(r.StartedAt):
			baseline = append(baseline, r)
		case r.StartedAt.After(baselineSpan.To):
			quiet = append(quiet, r)
		}
	}
	base, calm := windowStats("baseline", baseline), windowStats("quiet", quiet)
	tables := []requestWindowStats{base, calm}
	var checks []AssertionResult
	if base.Requests == 0 || base.P50 == 0 {
		checks = append(checks, invariantCheck("error-rate-and-latency.quiet_latency", caseID, false, "baseline window has completed requests", "none", fmt.Sprintf("baseline %s..%s", c.in.Settings.Baseline[0], c.in.Settings.Baseline[1])))
	} else {
		latencyOK := calm.Requests == 0 || (float64(calm.P50) <= quietP50Factor*float64(base.P50) && float64(calm.P99) <= quietP99Factor*float64(base.P99))
		checks = append(checks, invariantCheck("error-rate-and-latency.quiet_latency", caseID, latencyOK, fmt.Sprintf("p50 <= %.1fx %s, p99 <= %.0fx %s", quietP50Factor, base.P50, quietP99Factor, base.P99), fmt.Sprintf("p50=%s p99=%s requests=%d", calm.P50, calm.P99, calm.Requests), ""))
	}
	checks = append(checks,
		invariantCheck("error-rate-and-latency.quiet_error_rate", caseID, calm.ErrorRate <= quietErrorRate, fmt.Sprintf("error rate <= %.1f%%", quietErrorRate*100), fmt.Sprintf("%.2f%% (%d/%d)", calm.ErrorRate*100, calm.Errors, calm.Requests), ""),
		invariantCheck("error-rate-and-latency.quiet_empty_streams", caseID, calm.EmptyStreams == 0, "no streamed request ends empty outside fault windows", fmt.Sprintf("empty_streams=%d", calm.EmptyStreams), ""))
	for _, s := range spans {
		stats := windowStats(s.record.ID, perEvent[s.record.ID])
		tables = append(tables, stats)
		bound := eventErrorBound(s.record)
		passed := stats.ErrorRate <= bound
		expected := fmt.Sprintf("error rate <= %.0f%%", bound*100)
		if s.record.Action == "epoch_advance" {
			passed = passed && stats.NonRetryable == 0
			expected += ", errors only 503/429"
		}
		details := ""
		if overlaps := overlapsByEvent[s.record.ID]; len(overlaps) > 0 {
			details = "judged without the time it overlaps " + strings.Join(overlaps, ", ")
		}
		check := invariantCheck("error-rate-and-latency."+s.record.ID+".error_rate", s.record.Case, passed, expected, fmt.Sprintf("%.2f%% (%d/%d) non_503_429=%d", stats.ErrorRate*100, stats.Errors, stats.Requests, stats.NonRetryable), details)
		if stats.Requests == 0 {
			check.Status = statusNotJudged
		}
		checks = append(checks, withSpans(check, s.span))
	}
	for _, overlap := range slices.Sorted(maps.Keys(perOverlap)) {
		tables = append(tables, windowStats(overlap, perOverlap[overlap]))
	}
	if excluded > 0 {
		tables = append(tables, requestWindowStats{Window: "excluded_invalid", Requests: excluded})
	}
	return checks, tables
}

// ---- recovery-after-fault: recovery time after each event (Requests, Events) ----

func evalRecoveryAfterFault(c *invariantContext) ([]AssertionResult, any) {
	var checks []AssertionResult
	recoveries := map[string]string{}
	for _, record := range c.in.Events {
		if record.StartedAt == nil || isBackground(record.Params) {
			continue
		}
		end := *record.StartedAt
		if record.EndedAt != nil {
			end = *record.EndedAt
		}
		limit := recoveryLimit
		if record.Action == "gateway_restart" {
			limit = restartRecoveryLim
		}
		cutoff, cutBy := c.nextFaultAfter(record, end)
		recovered, observed, seen := time.Duration(-1), false, false
		for bucket := end; bucket.Before(c.runEnd) && !bucket.Add(recoveryBucket).After(cutoff); bucket = bucket.Add(recoveryBucket) {
			var inBucket []RequestResult
			for _, r := range c.in.Requests {
				if !r.StartedAt.Before(bucket) && r.StartedAt.Before(bucket.Add(recoveryBucket)) {
					inBucket = append(inBucket, r)
				}
			}
			stats := windowStats("", inBucket)
			if stats.Requests == 0 {
				continue
			}
			seen = true
			if stats.ErrorRate <= quietErrorRate {
				recovered, observed = bucket.Sub(end), true
				break
			}
		}
		actual := "never"
		cutShort := !observed && cutoff.Before(end.Add(limit))
		switch {
		case observed:
			actual = recovered.String()
		case cutShort:
			actual = fmt.Sprintf("not judged: %s came %s after the event", cutBy, cutoff.Sub(end).Round(time.Second))
		case !seen:
			actual = "no traffic after the event"
		}
		recoveries[record.ID] = actual
		check := invariantCheck("recovery-after-fault."+record.ID+".recovery", record.Case, (observed && recovered <= limit) || cutShort || !seen, fmt.Sprintf("first 30s bucket within quiet error bound starts <= %s after the event, before the next fault", limit), actual, "")
		if cutShort || !seen {
			check.Status = statusNotJudged
		}
		checks = append(checks, withSpans(check, timeSpan{From: *record.StartedAt, To: end.Add(limit)}))
	}
	return checks, recoveries
}

// ---- no-restarts-or-resource-growth: no goroutine or memory growth trend (Runtime metrics.jsonl) ----

type trendRow struct {
	Target          string `json:"target"`
	Samples         int    `json:"steady_samples"`
	Restarts        int    `json:"restarts"`
	AllowedRestarts int    `json:"allowed_restarts"`
	// SteadyFrom is set when a host started serving late, so its steady samples start later.
	SteadyFrom        string  `json:"steady_from,omitempty"`
	EscrowsCreated    int     `json:"escrows_created_between_windows,omitempty"`
	EarlyGoroutines   float64 `json:"early_goroutines_median"`
	LateGoroutines    float64 `json:"late_goroutines_median"`
	HeapSlopeBytesMin float64 `json:"heap_live_slope_bytes_per_min"`
	// HeapMinusCache is set when the heap slope was taken with devshard_gateway_cache_bytes subtracted.
	HeapMinusCache   bool    `json:"heap_minus_cache,omitempty"`
	RSSSlopeBytesMin float64 `json:"rss_slope_bytes_per_min"`
	Note             string  `json:"note,omitempty"`
}

// nextFaultAfter is the first moment from end on that another fault is
// active, or the run end; recovery after record is only judged before it.
// It uses EndedAt, not activeEnd: a restart's serving tail is error-rate-and-latency attribution.
func (c *invariantContext) nextFaultAfter(record EventRecord, end time.Time) (time.Time, string) {
	cutoff, cutBy := c.runEnd, "the workload end"
	for _, other := range c.in.Events {
		otherEnd := c.runEnd
		if other.EndedAt != nil {
			otherEnd = *other.EndedAt
		}
		if other.ID == record.ID || other.StartedAt == nil || isBackground(other.Params) || otherEnd.Before(end) {
			continue
		}
		start := *other.StartedAt
		if start.Before(end) {
			start = end
		}
		if start.Before(cutoff) {
			cutoff, cutBy = start, other.ID
		}
	}
	return cutoff, cutBy
}

func (c *invariantContext) allowedRestarts(target string) int {
	allowed := 0
	for _, record := range c.in.Events {
		switch {
		case target == "gateway" && record.Action == "gateway_restart":
			allowed++
		case target == record.Target+"/devshardd" && (record.Action == "container_restart" || record.Action == "container_kill" || record.Action == "tripfile"):
			allowed++
		}
	}
	return allowed
}

func evalNoRestartsOrResourceGrowth(c *invariantContext) ([]AssertionResult, any) {
	caseID := c.runCase("no-restarts-or-resource-growth")
	byTarget := map[string][]MetricsSample{}
	var escrowsCreated []time.Time
	firstSeen := map[string]bool{}
	for _, s := range sortedMetrics(c.in.Metrics) {
		if s.EscrowID != "" && !firstSeen[s.EscrowID] {
			firstSeen[s.EscrowID] = true
			escrowsCreated = append(escrowsCreated, s.At)
		}
		if s.EscrowID == "" && s.Target != "escrows" && len(s.Values) > 0 {
			byTarget[s.Target] = append(byTarget[s.Target], s)
		}
	}
	targets := make([]string, 0, len(byTarget))
	for target := range byTarget {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	var rows []trendRow
	var restartBad, goroutineBad, heapBad, heapNotJudged []string
	for _, target := range targets {
		samples := byTarget[target]
		host := target != "gateway"
		row := trendRow{Target: target, AllowedRestarts: c.allowedRestarts(target)}
		segments := [][]MetricsSample{{samples[0]}}
		for i := 1; i < len(samples); i++ {
			if processChanged(samples[i-1].Values, samples[i].Values) {
				row.Restarts++
				segments = append(segments, nil)
			}
			segments[len(segments)-1] = append(segments[len(segments)-1], samples[i])
		}
		if row.Restarts > row.AllowedRestarts {
			restartBad = append(restartBad, fmt.Sprintf("%s restarted %d times, %d injected", target, row.Restarts, row.AllowedRestarts))
		}
		// A host that serves nothing yet, such as the idle replica of an HA pair, is not warm:
		// its steady samples start SteadyAfter after its first finished request.
		steadyFrom := c.in.Started.Add(c.in.Settings.SteadyAfter)
		if serving, ok := firstServing(samples); host && ok && serving.Add(c.in.Settings.SteadyAfter).After(steadyFrom) {
			steadyFrom = serving.Add(c.in.Settings.SteadyAfter)
			row.SteadyFrom = steadyFrom.Sub(c.in.Started).Round(time.Second).String()
		}
		var steady []MetricsSample
		for _, segment := range segments {
			var candidate []MetricsSample
			for _, s := range segment {
				if s.Phase == "workload" && !s.At.Before(steadyFrom) {
					candidate = append(candidate, s)
				}
			}
			if len(candidate) > len(steady) {
				steady = candidate
			}
		}
		row.Samples = len(steady)
		earlyWindow, lateWindow := c.in.Settings.TrendEarly, c.in.Settings.TrendLate
		if shift := steadyFrom.Sub(c.in.Started) - earlyWindow[0]; shift > 0 {
			earlyWindow = [2]time.Duration{earlyWindow[0] + shift, earlyWindow[1] + shift}
		}
		early := c.metricWindow(steady, earlyWindow, "go_goroutines")
		late := c.metricWindow(steady, lateWindow, "go_goroutines")
		switch {
		case earlyWindow[1] > lateWindow[0]:
			row.Note = "serving started too late for an early window before the late one"
		case len(early) == 0 || len(late) == 0:
			row.Note = "early or late window has no samples in one process lifetime"
		default:
			row.EarlyGoroutines, row.LateGoroutines = median(early), median(late)
			if host {
				row.EscrowsCreated = countBetween(escrowsCreated, c.in.Started.Add(earlyWindow[0]), c.in.Started.Add(lateWindow[1]))
			}
			allowance := float64(hostValidationWorkers * row.EscrowsCreated)
			if row.LateGoroutines > goroutineGrowthMul*row.EarlyGoroutines+goroutineGrowthAdd+allowance {
				bad := fmt.Sprintf("%s goroutines %.0f -> %.0f", target, row.EarlyGoroutines, row.LateGoroutines)
				if host {
					bad += fmt.Sprintf(" (%d escrows created, +%.0f allowed for them)", row.EscrowsCreated, allowance)
				}
				goroutineBad = append(goroutineBad, bad)
			}
		}
		var queues []time.Time
		if host {
			queues = escrowsCreated
		}
		heap, minusCache, note := heapFloor(target, steady, queues, c.in.Started)
		row.HeapMinusCache = minusCache
		if note != "" {
			row.Note = strings.TrimPrefix(row.Note+"; "+note, "; ")
		}
		if slope, ok := c.seriesSlope(heap); ok {
			row.HeapSlopeBytesMin = slope
			switch {
			case !host && !minusCache:
				heapNotJudged = append(heapNotJudged, fmt.Sprintf("%s heap +%.0f B/min not judged: %s", target, slope, note))
			case slope >= heapSlopeLimit:
				heapBad = append(heapBad, fmt.Sprintf("%s live heap +%.0f B/min", target, slope))
			}
		}
		row.RSSSlopeBytesMin, _ = c.metricSlope(steady, "process_resident_memory_bytes")
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return []AssertionResult{invariantCheck("no-restarts-or-resource-growth.goroutines", caseID, false, "process samples in metrics.jsonl", "none", "")}, nil
	}
	heapCheck := invariantCheck("no-restarts-or-resource-growth.heap_slope", caseID, len(heapBad) == 0, "Theil-Sen slope < 1 MiB/min of the live heap (lowest heap_alloc per 30 s) over steady samples, less the gateway chat cache and each new escrow's host validation queue", fmt.Sprintf("violations=%d", len(heapBad)), strings.Join(append(heapBad, heapNotJudged...), "; "))
	if heapCheck.Passed && len(heapNotJudged) > 0 {
		heapCheck.Status = statusNotJudged
	}
	return []AssertionResult{
		invariantCheck("no-restarts-or-resource-growth.restarts", caseID, len(restartBad) == 0, "no process restarts except injected ones", fmt.Sprintf("violations=%d", len(restartBad)), strings.Join(restartBad, "; ")),
		invariantCheck("no-restarts-or-resource-growth.goroutines", caseID, len(goroutineBad) == 0, fmt.Sprintf("late median <= %.1fx early median + %d, + %d per escrow created between the windows on a host", goroutineGrowthMul, goroutineGrowthAdd, hostValidationWorkers), fmt.Sprintf("violations=%d", len(goroutineBad)), strings.Join(goroutineBad, "; ")),
		heapCheck,
	}, rows
}

// firstServing is the first sample at which a host has finished a request.
func firstServing(samples []MetricsSample) (time.Time, bool) {
	for _, s := range samples {
		if served, ok := domainSum(s, hostServingFamily); ok && served > 0 {
			return s.At, true
		}
	}
	return time.Time{}, false
}

// countBetween counts the times in (from, to].
func countBetween(times []time.Time, from, to time.Time) int {
	n := 0
	for _, t := range times {
		if t.After(from) && !t.After(to) {
			n++
		}
	}
	return n
}

func (c *invariantContext) metricWindow(samples []MetricsSample, window [2]time.Duration, name string) []float64 {
	from, to := c.in.Started.Add(window[0]), c.in.Started.Add(window[1])
	var values []float64
	for _, s := range samples {
		if value, ok := s.Values[name]; ok && !s.At.Before(from) && !s.At.After(to) {
			values = append(values, value)
		}
	}
	return values
}

func (c *invariantContext) metricSlope(samples []MetricsSample, name string) (float64, bool) {
	var points []metricPoint
	for _, s := range samples {
		if value, ok := s.Values[name]; ok {
			points = append(points, metricPoint{at: s.At, value: value})
		}
	}
	return c.seriesSlope(points)
}

func (c *invariantContext) seriesSlope(points []metricPoint) (float64, bool) {
	var xs, ys []float64
	for _, p := range points {
		xs = append(xs, p.at.Sub(c.in.Started).Minutes())
		ys = append(ys, p.value)
	}
	slope, _, ok := theilSen(xs, ys)
	return slope, ok
}

// heapFloor returns the live heap of each heapFloorBucket: the lowest heap_alloc in it, less
// devshard_gateway_cache_bytes and less the validation queue of every escrow in queues created
// after the first sample. The gateway chat cache fills to its cap and then stays flat, and a host
// keeps each escrow's queue for the epoch, so counting either reads bounded memory as growth. A
// gateway sample set where any sample lacks the cache series is left whole, with a note.
func heapFloor(target string, samples []MetricsSample, queues []time.Time, started time.Time) ([]metricPoint, bool, string) {
	var heap []metricPoint
	missing := 0
	for _, s := range samples {
		value, ok := s.Values["go_memstats_heap_alloc_bytes"]
		if !ok {
			continue
		}
		if cache, ok := domainSum(s, gatewayCacheFamily); ok {
			value -= cache
		} else {
			missing++
		}
		value -= float64(hostValidationQueueBytes * countBetween(queues, samples[0].At, s.At))
		bucket := started.Add(s.At.Sub(started).Truncate(heapFloorBucket))
		if n := len(heap); n > 0 && heap[n-1].at.Equal(bucket) {
			heap[n-1].value = min(heap[n-1].value, value)
		} else {
			heap = append(heap, metricPoint{at: bucket, value: value})
		}
	}
	if len(heap) > 0 && missing == 0 {
		return heap, true, ""
	}
	if target != "gateway" || len(heap) == 0 {
		return heap, false, ""
	}
	return heap, false, fmt.Sprintf("%s missing in %d steady samples, so the chat cache (1 h TTL, 256 MiB cap) cannot be told from growth", gatewayCacheFamily, missing)
}

// ---- faults-fired-and-ml-nodes-bound: every fault actually fired (all sources) ----

func evalFaultsFiredAndMLNodesBound(c *invariantContext) ([]AssertionResult, any) {
	var checks []AssertionResult
	signals := map[string]string{}
	for _, record := range c.in.Events {
		applied := record.StartedAt != nil && record.Error == ""
		signal, ok := "not applied", false
		if applied {
			signal, ok = c.firedSignal(record)
		}
		signals[record.ID] = signal
		// No Case: a fault that never fired invalidates the run, so a
		// known_bug marker must not turn it into XFAIL.
		details := ""
		if record.Case != "" {
			details = "case " + record.Case
		}
		if record.Error != "" {
			details = strings.TrimPrefix(details+"; "+record.Error, "; ")
		}
		check := invariantCheck("faults-fired-and-ml-nodes-bound."+record.ID+".fired", "", applied && ok, "event applied and its effect observed", signal, details)
		if applied {
			end := c.runEnd
			if record.EndedAt != nil {
				end = *record.EndedAt
			}
			check = withSpans(check, timeSpan{From: *record.StartedAt, To: end.Add(c.in.Settings.Grace)})
		}
		checks = append(checks, check)
	}
	if check, ok := c.hostBindingCheck(); ok {
		checks = append(checks, check)
	}
	return checks, signals
}

// boundHosts lists the hosts the scenario binds at least one ML node to.
func boundHosts(s Scenario) map[string]bool {
	out := map[string]bool{}
	for _, node := range s.Topology.MockML.Nodes {
		if node.Host != "" {
			out[node.Host] = true
		}
	}
	return out
}

// bindingProblems says why the ML-node binding is not in effect: unmapped
// allocations since base (nil: since start), or no bound host allocated at all.
// A bound host may legitimately stay idle, e.g. an HA standby.
func bindingProblems(base *DapiHostSample, last DapiHostSample, bound map[string]bool) []string {
	var problems []string
	unmapped := last.Hosts[dapiUnmappedHost].Allocations
	if base != nil {
		unmapped -= min(unmapped, base.Hosts[dapiUnmappedHost].Allocations)
	}
	if unmapped > 0 {
		problems = append(problems, fmt.Sprintf("%d unmapped allocations", unmapped))
	}
	var mapped uint64
	for host := range bound {
		mapped += last.Hosts[host].Allocations + last.Hosts[host].ChallengeRejections
	}
	if mapped == 0 {
		problems = append(problems, "no bound host acquired a node")
	}
	return problems
}

func (c *invariantContext) usableDapi() []DapiHostSample {
	var out []DapiHostSample
	for _, s := range c.in.DapiHosts {
		if s.Error == "" && s.Hosts != nil {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// hostBindingCheck is the RC-A meta-check: with ML nodes bound to hosts,
// mock-dapi must attribute every allocation after warm-up to a host.
func (c *invariantContext) hostBindingCheck() (AssertionResult, bool) {
	bound := map[string]bool{}
	for _, host := range c.in.NodeHosts {
		if host != "" {
			bound[host] = true
		}
	}
	if len(bound) == 0 {
		return AssertionResult{}, false
	}
	const expected = "mock-dapi host-stats: 0 unmapped allocations after warm-up"
	samples := c.usableDapi()
	if len(samples) == 0 {
		return invariantCheck("faults-fired-and-ml-nodes-bound.host_binding", "", false, expected, "unavailable", "no usable mock-dapi /testenv/host-stats sample"), true
	}
	warmup := c.in.Started.Add(c.in.Settings.SteadyAfter)
	var base *DapiHostSample
	for i := range samples {
		if !samples[i].At.Before(warmup) {
			base = &samples[i]
			break
		}
	}
	if base == nil {
		return invariantCheck("faults-fired-and-ml-nodes-bound.host_binding", "", false, expected, "unavailable", "no mock-dapi sample after warm-up"), true
	}
	last := samples[len(samples)-1]
	problems := bindingProblems(base, last, bound)
	actual := fmt.Sprintf("unmapped=%d (%d at warm-up)", last.Hosts[dapiUnmappedHost].Allocations, base.Hosts[dapiUnmappedHost].Allocations)
	return invariantCheck("faults-fired-and-ml-nodes-bound.host_binding", "", len(problems) == 0, expected, actual, strings.Join(problems, "; ")), true
}

// dapiIncrease is the growth of a cumulative mock-dapi counter over span:
// the highest value inside it minus the last value before it.
func (c *invariantContext) dapiIncrease(span timeSpan, value func(map[string]DapiHostRow) uint64) uint64 {
	var before, after uint64
	for _, s := range c.usableDapi() {
		v := value(s.Hosts)
		if s.At.Before(span.From) {
			before = v
		} else if span.contains(s.At) {
			after = max(after, v)
		}
	}
	if after < before {
		return 0
	}
	return after - before
}

// mlNodeServed checks through mock-dapi that a faulted node was acquired by
// its own host during the window and by no other caller.
func (c *invariantContext) mlNodeServed(node, host string, span timeSpan) (string, bool) {
	own := c.dapiIncrease(span, func(hosts map[string]DapiHostRow) uint64 { return hosts[host].NodeAllocations[node] })
	var others []string
	seen := map[string]bool{}
	for _, s := range c.usableDapi() {
		for name := range s.Hosts {
			if name == host || seen[name] {
				continue
			}
			seen[name] = true
			if n := c.dapiIncrease(span, func(hosts map[string]DapiHostRow) uint64 { return hosts[name].NodeAllocations[node] }); n > 0 {
				others = append(others, fmt.Sprintf("%s=%d", name, n))
			}
		}
	}
	sort.Strings(others)
	if len(others) > 0 {
		return fmt.Sprintf("%s also acquired by %s; binding not in effect", node, strings.Join(others, ",")), false
	}
	if own == 0 {
		return fmt.Sprintf("%s not acquired by %s in the window", node, host), false
	}
	return "", true
}

// firedSignal looks for the positive effect of an applied event. Actions
// whose effect no collected source shows pass on the apply alone.
func (c *invariantContext) firedSignal(record EventRecord) (string, bool) {
	start := *record.StartedAt
	end := c.runEnd
	if record.EndedAt != nil {
		end = *record.EndedAt
	}
	graced := timeSpan{From: start, To: end.Add(c.in.Settings.Grace)}
	_, participant := c.eventTarget(record)
	switch record.Action {
	case "ml_fault":
		if participant == "" {
			return "ML node has no host binding, so the fault hits every participant", false
		}
		if len(c.usableDapi()) > 0 {
			if signal, ok := c.mlNodeServed(record.Target, c.in.NodeHosts[record.Target], graced); !ok {
				return signal, false
			}
		}
		for _, s := range c.in.Throttle {
			if !graced.contains(s.At) {
				continue
			}
			for _, p := range s.Participants {
				if p.ParticipantKey == participant && (p.FailureStrikes > 0 || p.inQuarantine()) {
					return "throttle strikes or quarantine observed", true
				}
			}
		}
		for _, t := range c.quarantineTransitions() {
			if t.Participant == participant && graced.contains(t.At) {
				return "quarantine transition " + t.Reason, true
			}
		}
		if c.attemptFailuresIncreased(participant, graced) {
			return "attempt failures increased", true
		}
		return "no strike, quarantine or attempt failure for " + participant, false
	case "dapi_challenge":
		if len(c.usableDapi()) == 0 {
			return "no mock-dapi host-stats to show challenge rejections", false
		}
		span := timeSpan{From: start, To: end.Add(dapiHostsPollEvery)}
		if n := c.dapiIncrease(span, func(hosts map[string]DapiHostRow) uint64 { return hosts[record.Target].ChallengeRejections }); n > 0 {
			return fmt.Sprintf("%d challenge rejections for %s", n, record.Target), true
		}
		return "no challenge rejection for " + record.Target, false
	case "gateway_restart":
		if c.processChangedIn("gateway", graced) {
			return "gateway process changed", true
		}
		return "gateway PID and start ticks unchanged", false
	case "container_restart", "container_kill":
		if c.processChangedIn(record.Target+"/devshardd", graced) {
			return "devshardd process changed", true
		}
		return "devshardd PID and start ticks unchanged", false
	case "epoch_advance":
		before, seen := "", map[string]bool{}
		for _, s := range sortedMetrics(c.in.Metrics) {
			if s.EscrowID == "" || s.ChainPhase == "" {
				continue
			}
			if s.At.Before(start) {
				before = s.ChainPhase
			} else if graced.contains(s.At) {
				seen[s.ChainPhase] = true
			}
		}
		for phase := range seen {
			if phase != before {
				return "gateway saw chain phase " + phase, true
			}
		}
		return "gateway chain phase did not change", false
	}
	if observe, _ := record.Params["observe"].(string); record.Action == "marker" && observe == "host_rate_limited" {
		if n := c.domainIncrease(record.Target+"/devshardd", graced, isHostRateLimited); n > 0 {
			return fmt.Sprintf("%s: %.0f rate_limited no-receipt terminals", record.Target, n), true
		}
		return record.Target + ": no rate_limited no-receipt terminal in the window", false
	}
	return "applied only; no collected source shows " + record.Action, true
}

func isHostRateLimited(name string, labels map[string]string) bool {
	return name == "devshard_request_terminal_total" && labels["reason"] == "rate_limited" && labels["terminal"] == "no_receipt_interrupted"
}

// domainIncrease sums the increase of matching Domain series of target over
// span; a counter that drops (process restart) restarts from zero.
func (c *invariantContext) domainIncrease(target string, span timeSpan, match func(string, map[string]string) bool) float64 {
	increase := 0.0
	previous := map[string]float64{}
	for _, s := range sortedMetrics(c.in.Metrics) {
		if s.Target != target || s.Domain == nil {
			continue
		}
		for key, value := range s.Domain {
			name, labels := parseDomainKey(key)
			if !match(name, labels) {
				continue
			}
			old, seen := previous[key]
			previous[key] = value
			if seen && span.contains(s.At) {
				if value < old {
					old = 0
				}
				increase += value - old
			}
		}
	}
	return increase
}

func (c *invariantContext) processChangedIn(target string, span timeSpan) bool {
	var previous map[string]float64
	for _, s := range sortedMetrics(c.in.Metrics) {
		if s.Target != target || len(s.Values) == 0 {
			continue
		}
		if previous != nil && span.contains(s.At) && processChanged(previous, s.Values) {
			return true
		}
		previous = s.Values
	}
	return false
}

func (c *invariantContext) attemptFailuresIncreased(participant string, span timeSpan) bool {
	before, after := 0.0, 0.0
	for _, s := range sortedMetrics(c.in.Metrics) {
		if s.Target != "gateway" {
			continue
		}
		total := 0.0
		for key, value := range s.Domain {
			name, labels := parseDomainKey(key)
			if name == "devshard_gateway_attempt_failures_total" && labels["participant_key"] == participant {
				total += value
			}
		}
		if s.At.Before(span.From) {
			before = total
		} else if span.contains(s.At) {
			after = max(after, total)
		}
	}
	return after > before
}

// ---- no-skipped-validation-leases: no owed validation skipped (Postgres lease table; sqlite hosts excluded) ----

func evalNoSkippedValidationLeases(c *invariantContext) ([]AssertionResult, any) {
	caseID := c.runCase("no-skipped-validation-leases")
	expected := "0 skipped validation leases for honest hosts"
	if excluded := c.leaseExcluded(); excluded != "" {
		expected += "; " + excluded
	}
	if c.in.Leases == nil {
		if c.in.LeasesError == "" {
			return nil, nil
		}
		return []AssertionResult{invariantCheck("no-skipped-validation-leases.no_skipped_leases", caseID, false, "lease counts available", "unavailable", c.in.LeasesError)}, map[string]any{"excluded_hosts": c.in.LeaseExcluded}
	}
	skipped := map[string]int{}
	var bad []string
	for _, row := range c.in.Leases {
		if row.Status != "skipped" || row.Count == 0 {
			continue
		}
		participant, known := c.addressParticipant(row.InstanceAddress)
		skipped[row.InstanceAddress] += row.Count
		if !known || c.honest[participant] {
			bad = append(bad, fmt.Sprintf("%s skipped=%d", row.InstanceAddress, row.Count))
		}
	}
	return []AssertionResult{
		invariantCheck("no-skipped-validation-leases.no_skipped_leases", caseID, len(bad) == 0, expected, fmt.Sprintf("violations=%d", len(bad)), strings.Join(bad, "; ")),
	}, map[string]any{"skipped_by_instance": skipped, "excluded_hosts": c.in.LeaseExcluded}
}

// leaseExcluded names the hosts no-skipped-validation-leases cannot see, sorted, e.g. "versiond-4 excluded (sqlite ...)".
func (c *invariantContext) leaseExcluded() string {
	hosts := make([]string, 0, len(c.in.LeaseExcluded))
	for host := range c.in.LeaseExcluded {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	parts := make([]string, 0, len(hosts))
	for _, host := range hosts {
		parts = append(parts, fmt.Sprintf("%s excluded (%s)", host, c.in.LeaseExcluded[host]))
	}
	return strings.Join(parts, ", ")
}

// addressParticipant maps a lease instance address to its participant; HA
// replicas share the participant identity.
func (c *invariantContext) addressParticipant(address string) (string, bool) {
	for _, host := range c.in.Hosts {
		if host.Participant == address || host.ID == address {
			return host.Participant, true
		}
	}
	return "", false
}

// ---- no-admission-during-poc: no admission during PoC-blocking phases (GW-dbg inferences, Events) ----

func evalNoAdmissionDuringPoC(c *invariantContext) ([]AssertionResult, any) {
	var checks []AssertionResult
	for _, record := range c.in.Events {
		if record.Action != "epoch_advance" || record.StartedAt == nil || record.EndedAt == nil {
			continue
		}
		if c.in.Inferences == nil {
			checks = append(checks, invariantCheck("no-admission-during-poc."+record.ID+".no_admission", record.Case, false, "gateway inferences available", "unavailable", ""))
			continue
		}
		// started_at has one-second resolution, so trim a second from each edge,
		// and one phase poll from the start.
		from, to := record.StartedAt.Add(time.Second+gatewayPhasePoll), record.EndedAt.Add(-time.Second)
		admitted := 0
		for _, inferences := range c.in.Inferences {
			for _, inference := range inferences {
				t := time.Unix(inference.StartedAt, 0)
				if inference.StartedAt > 0 && t.After(from) && t.Before(to) {
					admitted++
				}
			}
		}
		checks = append(checks, invariantCheck("no-admission-during-poc."+record.ID+".no_admission", record.Case, admitted == 0, "0 nonces started inside the blocking window", fmt.Sprintf("started=%d", admitted), ""))
	}
	return checks, nil
}

// ---- responses-compress: responses compress (compose.log) ----

func evalResponsesCompress(c *invariantContext) ([]AssertionResult, any) {
	caseID := c.runCase("responses-compress")
	if c.in.DidNotCompress < 0 {
		return []AssertionResult{invariantCheck("responses-compress.responses_compress", caseID, false, "compose.log readable", "unavailable", "")}, nil
	}
	return []AssertionResult{invariantCheck("responses-compress.responses_compress", caseID, c.in.DidNotCompress == 0, "0 'did not compress' log lines", fmt.Sprintf("lines=%d", c.in.DidNotCompress), "")}, nil
}

// ---- payload-files-are-zstd: payload files are zstd on sqlite hosts (docker exec sample of {data-dir}/payloads) ----

func evalPayloadFilesAreZstd(c *invariantContext) ([]AssertionResult, any) {
	if len(c.in.PayloadFiles) == 0 {
		return nil, nil
	}
	caseID := c.runCase("payload-files-are-zstd")
	hosts := make([]string, 0, len(c.in.PayloadFiles))
	for host := range c.in.PayloadFiles {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	var checks []AssertionResult
	for _, host := range hosts {
		stats := c.in.PayloadFiles[host]
		var problems []string
		if stats.Error != "" {
			problems = append(problems, "sample: "+stats.Error)
		}
		// An empty dir means the file path never ran, which is a failure, not a skip.
		if stats.Files < payloadSampleMin {
			problems = append(problems, fmt.Sprintf("%d payload files, want at least %d", stats.Files, payloadSampleMin))
		}
		if len(stats.NotZstd) > 0 {
			problems = append(problems, fmt.Sprintf("%d files without the zstd magic, first %s", len(stats.NotZstd), stats.NotZstd[0]))
		}
		if stats.Compressed >= stats.Decompressed {
			problems = append(problems, "compressed size is not below decompressed size")
		}
		if stats.SampleError != "" {
			problems = append(problems, "decode: "+stats.SampleError)
		}
		actual := fmt.Sprintf("files=%d not_zstd=%d compressed=%d decompressed=%d", stats.Files, len(stats.NotZstd), stats.Compressed, stats.Decompressed)
		checks = append(checks, invariantCheck("payload-files-are-zstd."+host+".payload_files_zstd", caseID, len(problems) == 0,
			fmt.Sprintf(">= %d payload files, all zstd, smaller than decompressed, decoding to payload JSON", payloadSampleMin), actual, strings.Join(problems, "; ")))
	}
	return checks, c.in.PayloadFiles
}

// ---- no-http-5xx-on-honest-hosts: honest hosts get no http_5xx attempt failures (GW-prom; from #1988) ----

// isHTTP5xxFailure matches the gateway attempt-failure reasons a 5xx maps to:
// http_503, and http_error for any other status, a 500 included.
func isHTTP5xxFailure(reason string) bool {
	return reason == "http_503" || reason == "http_error"
}

func evalNoHTTP5xxOnHonestHosts(c *invariantContext) ([]AssertionResult, any) {
	caseID := c.runCase("no-http-5xx-on-honest-hosts")
	var global []timeSpan
	for _, w := range c.allEventSpans() {
		if _, participant := c.eventTarget(w.record); participant == "" {
			global = append(global, w.span)
		}
	}
	increases := map[string]float64{}
	var hits []string
	var spans []timeSpan
	previous := map[string]float64{}
	var previousAt time.Time
	for _, s := range sortedMetrics(c.in.Metrics) {
		if s.Target != "gateway" || s.Domain == nil {
			continue
		}
		for key, value := range s.Domain {
			name, labels := parseDomainKey(key)
			participant := labels["participant_key"]
			if name != "devshard_gateway_attempt_failures_total" || !c.honest[participant] || !isHTTP5xxFailure(labels["reason"]) {
				continue
			}
			old, seen := previous[key]
			previous[key] = value
			if !seen {
				continue
			}
			if value < old {
				old = 0
			}
			delta := timeSpan{From: previousAt, To: s.At}
			if value == old || overlapsAnyAfter(delta, global) {
				continue
			}
			increases[participant+"/"+labels["reason"]] += value - old
			hits = append(hits, fmt.Sprintf("%s %s +%.0f at %s", participant, labels["reason"], value-old, s.At.Format(time.RFC3339)))
			spans = append(spans, delta)
		}
		previousAt = s.At
	}
	sort.Strings(hits)
	return []AssertionResult{
		withSpans(invariantCheck("no-http-5xx-on-honest-hosts.honest_no_http_5xx", caseID, len(hits) == 0, "honest participants: 0 new http_5xx attempt failures outside global event windows + grace", fmt.Sprintf("violations=%d", len(hits)), strings.Join(firstN(hits, 10), "; ")), spans...),
	}, increases
}

func overlapsAnyAfter(delta timeSpan, spans []timeSpan) bool {
	for _, s := range spans {
		if s.overlapsAfter(delta.From, delta.To) {
			return true
		}
	}
	return false
}

// ---- no-escrow-falsely-marked-missing: no escrow marked inactive as missing while mock-chain has it (compose log, Chain; gateway-restart-while-participant-absent) ----

func evalNoEscrowFalselyMarkedMissing(c *invariantContext) ([]AssertionResult, any) {
	if c.in.EscrowMissing == nil {
		return nil, nil
	}
	caseID := c.runCase("no-escrow-falsely-marked-missing")
	if c.in.ChainError != "" {
		return []AssertionResult{invariantCheck("no-escrow-falsely-marked-missing.no_false_escrow_missing", caseID, false, "mock-chain escrow table available", "unavailable", c.in.ChainError)}, nil
	}
	onChain := map[string]bool{}
	for _, e := range c.in.Escrows {
		onChain[strconv.FormatUint(e.ID, 10)] = true
	}
	var bad []string
	for _, id := range c.in.EscrowMissing {
		if onChain[id] {
			bad = append(bad, id)
		}
	}
	return []AssertionResult{
		invariantCheck("no-escrow-falsely-marked-missing.no_false_escrow_missing", caseID, len(bad) == 0, "no escrow marked inactive as \"escrow missing on chain\" while mock-chain still has it",
			fmt.Sprintf("violations=%d lines=%d", len(bad), len(c.in.EscrowMissing)), strings.Join(bad, "; ")),
	}, map[string]any{"escrow_missing": c.in.EscrowMissing}
}
