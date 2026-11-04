package loadtest

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func hasPoCWindows(events []Event) bool {
	for _, e := range events {
		if e.Action == "poc_window" {
			return true
		}
	}
	return false
}

type pocParticipantReport struct {
	Participant string  `json:"participant"`
	Preserved   bool    `json:"preserved"`
	Assigned    *uint64 `json:"assigned_nonces"`
	Misses      *uint64 `json:"protocol_misses"`
	HostMissed  *uint64 `json:"host_missed"`
	MLRequests  *uint64 `json:"ml_requests_interior"`
	MLAborted   *uint64 `json:"ml_aborted"`
	Allocations *uint64 `json:"allocations_interior"`
}

type pocWindowReport struct {
	Window              string                 `json:"window"`
	From                time.Time              `json:"from"`
	To                  time.Time              `json:"to"`
	Completed           int                    `json:"completed"`
	Errors              int                    `json:"errors"`
	Dropped             int                    `json:"dropped_by_generator"`
	RPS                 float64                `json:"completed_cohort_rps"`
	P95                 time.Duration          `json:"p95"`
	P99                 time.Duration          `json:"p99"`
	Nonces              *uint64                `json:"sampled_nonce_delta"`
	NoncesPerCompletion *float64               `json:"nonces_per_completion"`
	Burns               int                    `json:"burned_without_send"`
	AccountingFrom      *time.Time             `json:"accounting_from,omitempty"`
	AccountingTo        *time.Time             `json:"accounting_to,omitempty"`
	Participants        []pocParticipantReport `json:"participants"`
}

func uintDelta(first, last uint64) *uint64 {
	if last < first {
		return nil
	}
	d := last - first
	return &d
}

// Report planned traffic windows; assertions use the recorded applied window.
// Counter deltas use samples inside a window; absent or reset counters stay nil.
func evalPoCLoad(c *invariantContext) ([]AssertionResult, any) {
	var events []EventRecord
	for _, e := range c.in.Events {
		if e.Action == "poc_window" {
			events = append(events, e)
		}
	}
	if len(events) == 0 {
		return nil, nil
	}
	sort.Slice(events, func(i, j int) bool { return events[i].PlannedStart.Before(events[j].PlannedStart) })
	var checks []AssertionResult
	var rows []pocWindowReport
	from := c.in.Started
	for i, e := range events {
		if e.PlannedStart.After(from) {
			name := "baseline"
			if i > 0 {
				name = "recovery"
			}
			quiet := pocWindowSummary(c, name, from, e.PlannedStart, "")
			rows = append(rows, quiet)
			for _, p := range quiet.Participants {
				checks = append(checks, invariantCheck("poc-load."+name+"."+p.Participant+".serves", "", p.MLRequests != nil && *p.MLRequests > 0, "> 0 ML requests per participant", pocUint(p.MLRequests), "HA replicas are aggregated"))
			}
		}
		preserved, _ := e.Params["preserved"].(string)
		row := pocWindowSummary(c, e.ID, e.PlannedStart, e.PlannedEnd, c.hostToPart[preserved])
		rows = append(rows, row)
		from = e.PlannedEnd
		if e.StartedAt == nil || e.EndedAt == nil {
			continue
		} // events.applied owns this failure
		// Skip one long-poll/sample interval at the edges. Refusals/probes are
		// permitted; a new ML request on a non-preserved participant is not.
		interiorFrom, interiorTo := e.StartedAt.Add(15*time.Second), e.EndedAt.Add(-5*time.Second)
		for _, participant := range c.participants {
			ml, alloc, aborted := pocMLDeltas(c, participant, interiorFrom, interiorTo, *e.StartedAt)
			prefix := "poc-load." + e.ID + "." + participant
			if participant == c.hostToPart[preserved] {
				checks = append(checks, invariantCheck(prefix+".preserved_serves", e.Case, ml != nil && *ml > 0, "preserved ML requests > 0", pocUint(ml), "interior samples"))
			} else {
				checks = append(checks, invariantCheck(prefix+".no_new_ml", e.Case, ml != nil && *ml == 0 && alloc != nil && *alloc == 0, "0 ML requests and 0 allocations", fmt.Sprintf("ML=%s allocations=%s", pocUint(ml), pocUint(alloc)), "interior samples; probes/refusals may continue"))
				checks = append(checks, invariantCheck(prefix+".active_aborted", e.Case, aborted != nil && *aborted > 0, "> 0 active ML requests aborted", pocUint(aborted), "fails if transition had no active work"))
			}
		}
		completed := 0
		for _, r := range c.in.Requests {
			if !r.StartedAt.Before(interiorFrom) && r.StartedAt.Before(interiorTo) && r.Outcome == "completed" {
				completed++
			}
		}
		checks = append(checks, invariantCheck("poc-load."+e.ID+".continues", e.Case, completed > 0, "> 0 completed user requests", fmt.Sprint(completed), "requests started inside applied PoC window"))
	}
	if from.Before(c.runEnd) {
		rows = append(rows, pocWindowSummary(c, "recovery-final", from, c.runEnd, ""))
	}
	return checks, rows
}

func pocWindowSummary(c *invariantContext, name string, from, to time.Time, preserved string) pocWindowReport {
	row := pocWindowReport{Window: name, From: from, To: to}
	var latencies []time.Duration
	for _, r := range c.in.Requests {
		if r.StartedAt.Before(from) || !r.StartedAt.Before(to) {
			continue
		}
		switch r.Outcome {
		case "dropped_by_generator":
			row.Dropped++
		case "completed":
			row.Completed++
			latencies = append(latencies, r.Duration)
		default:
			row.Errors++
		}
	}
	row.RPS = float64(row.Completed) / to.Sub(from).Seconds()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	row.P95, row.P99 = percentile(latencies, .95), percentile(latencies, .99)
	for _, b := range c.in.Burns {
		if !b.At.Before(from) && b.At.Before(to) {
			row.Burns++
		}
	}
	// Per-escrow spans prevent rotations from looking like a counter reset.
	first, last := map[string]MetricsSample{}, map[string]MetricsSample{}
	for _, s := range c.in.Metrics {
		if s.Nonce == nil || s.At.Before(from) || !s.At.Before(to) {
			continue
		}
		if old, ok := first[s.EscrowID]; !ok || s.At.Before(old.At) {
			first[s.EscrowID] = s
		}
		if old, ok := last[s.EscrowID]; !ok || s.At.After(old.At) {
			last[s.EscrowID] = s
		}
	}
	var total uint64
	valid := len(first) > 0
	for id, a := range first {
		b := last[id]
		d := uintDelta(*a.Nonce, *b.Nonce)
		if d == nil || !b.At.After(a.At) {
			valid = false
			break
		}
		total += *d
	}
	if valid {
		row.Nonces = &total
		if row.Completed > 0 {
			ratio := float64(total) / float64(row.Completed)
			row.NoncesPerCompletion = &ratio
		}
	}
	var a, b *AccountingSnapshot
	for i := range c.in.Accounting {
		s := &c.in.Accounting[i]
		if s.Error != "" || s.At.Before(from) || !s.At.Before(to) {
			continue
		}
		if a == nil || s.At.Before(a.At) {
			a = s
		}
		if b == nil || s.At.After(b.At) {
			b = s
		}
	}
	if a != nil && b != nil && b.At.After(a.At) && a.EpochIndex == b.EpochIndex {
		row.AccountingFrom = &a.At
		row.AccountingTo = &b.At
	}
	for _, participant := range c.participants {
		p := pocParticipantReport{Participant: participant, Preserved: participant == preserved}
		if row.AccountingFrom != nil {
			av, aok := pocAccountingParticipant(a, participant)
			bv, bok := pocAccountingParticipant(b, participant)
			if aok && bok {
				p.Assigned = uintDelta(av.AssignedNonces, bv.AssignedNonces)
				p.Misses = uintDelta(av.ProtocolMisses, bv.ProtocolMisses)
				p.HostMissed = uintDelta(av.HostMissed, bv.HostMissed)
			}
		}
		p.MLRequests, p.Allocations, p.MLAborted = pocMLDeltas(c, participant, from.Add(15*time.Second), to.Add(-5*time.Second), from)
		row.Participants = append(row.Participants, p)
	}
	return row
}

func pocAccountingParticipant(s *AccountingSnapshot, participant string) (accountingTotals, bool) {
	for _, p := range s.Participants {
		if p.Participant == participant {
			return sumAccounting(accountingAPIResponse{Participants: s.Participants}, participant), true
		}
	}
	return accountingTotals{}, false
}

// ML-aborted deltas straddle entry; admission deltas lie strictly inside.
func pocMLDeltas(c *invariantContext, participant string, from, to, entry time.Time) (*uint64, *uint64, *uint64) {
	var a, b, before *DapiHostSample
	for i := range c.in.DapiHosts {
		s := &c.in.DapiHosts[i]
		if s.Error != "" {
			continue
		}
		// A poll is timestamped before its HTTP reads. A boundary poll can
		// already contain post-transition counters; keep it out of the
		// pre-entry baseline rather than subtracting away the abort itself.
		if !s.At.After(entry.Add(-time.Second)) && (before == nil || s.At.After(before.At)) {
			before = s
		}
		if s.At.Before(from) || !s.At.Before(to) {
			continue
		}
		if a == nil || s.At.Before(a.At) {
			a = s
		}
		if b == nil || s.At.After(b.At) {
			b = s
		}
	}
	if a == nil || b == nil || !b.At.After(a.At) {
		return nil, nil, nil
	}
	var ml, alloc, aborted uint64
	mlOK, allocOK, abortOK := true, true, before != nil
	foundNode, foundHost := false, false
	for node, host := range c.in.NodeHosts {
		if c.hostToPart[host] != participant {
			continue
		}
		foundNode = true
		av, aok := a.MLNodes[node]
		bv, bok := b.MLNodes[node]
		if !aok || !bok || bv.RequestsReceived < av.RequestsReceived {
			mlOK = false
		} else {
			ml += bv.RequestsReceived - av.RequestsReceived
		}
		if before != nil {
			v, ok := before.MLNodes[node]
			if !ok || !bok || bv.AbortedRequests < v.AbortedRequests {
				abortOK = false
			} else {
				aborted += bv.AbortedRequests - v.AbortedRequests
			}
		}
	}
	for host, part := range c.hostToPart {
		if part != participant {
			continue
		}
		foundHost = true
		av, aok := a.Hosts[host]
		bv, bok := b.Hosts[host]
		if !aok || !bok || bv.Allocations < av.Allocations {
			allocOK = false
		} else {
			alloc += bv.Allocations - av.Allocations
		}
	}
	var mp, ap, bp *uint64
	if mlOK && foundNode {
		mp = &ml
	}
	if allocOK && foundHost {
		ap = &alloc
	}
	if abortOK && foundNode {
		bp = &aborted
	}
	return mp, ap, bp
}

func pocUint(v *uint64) string {
	if v == nil {
		return reportMissing
	}
	return fmt.Sprint(*v)
}

func (r runReport) renderPoC(out *strings.Builder) {
	if len(r.poc) == 0 {
		return
	}
	out.WriteString("\n## PoC load windows\n\nRPS counts eventual completions of requests started in the window. Nonce and accounting deltas cover sampled interior spans; missing/reset counters are —. Burns include all committed nonces never sent.\n\n")
	out.WriteString("| Window | Completed RPS | Errors | Generator drops | p95 | p99 | Δnonce (sampled) | Nonces/completion (approx.) | Burns |\n| --- | ---: | ---: | ---: | --- | --- | ---: | ---: | ---: |\n")
	for _, w := range r.poc {
		ratio := reportMissing
		if w.NoncesPerCompletion != nil {
			ratio = fmt.Sprintf("%.2f", *w.NoncesPerCompletion)
		}
		fmt.Fprintf(out, "| %s | %.2f | %d | %d | %s | %s | %s | %s | %d |\n", w.Window, w.RPS, w.Errors, w.Dropped, w.P95.Round(time.Millisecond), w.P99.Round(time.Millisecond), pocUint(w.Nonces), ratio, w.Burns)
	}
	out.WriteString("\n| Window | Participant | Preserved | Assigned (sampled) | Protocol misses | Host missed | ML requests (interior) | Aborted ML |\n| --- | --- | --- | ---: | ---: | ---: | ---: | ---: |\n")
	for _, w := range r.poc {
		for _, p := range w.Participants {
			fmt.Fprintf(out, "| %s | %s | %t | %s | %s | %s | %s | %s |\n", w.Window, p.Participant, p.Preserved, pocUint(p.Assigned), pocUint(p.Misses), pocUint(p.HostMissed), pocUint(p.MLRequests), pocUint(p.MLAborted))
		}
	}
}
