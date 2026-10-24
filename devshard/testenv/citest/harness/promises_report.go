package harness

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
)

// PromiseDescription says in one line what a check promises and what it reads.
type PromiseDescription struct {
	Promise string
	Reads   string
}

// PromiseCatalog names every check CheckPromises reports, in report order.
var PromiseCatalog = []struct {
	Name string
	PromiseDescription
}{
	{InferenceClosesInTime, PromiseDescription{"Every started inference reaches Finish, Timeout or ErrorMiss before its deadline plus slack", "each host's diff chain, export time"}},
	{ServedRequestFinished, PromiseDescription{"A request the client got 200 for has a Finish on every exported host's chain", "client request log, each host's diff chain"}},
	{PaidOutputWithinProduced, PromiseDescription{"A paid Finish.OutputTokens equals the tokens the ML execution produced and never exceeds max_tokens. A Finish whose inference was invalidated paid nothing: its overclaim is caught and counted apart", "MsgStartInference, MsgFinishInference, the votes and usage challenges of both phases in the diffs, mock ML log"}},
	{EscrowMoneyConserved, PromiseDescription{"The escrow balance adds up: deposited = spent + reserved + left + fees, replayed through the real state machine", "each host's diff chain"}},
	{GatewayHeapBounded, PromiseDescription{"Gateway heap growth per request stays within k times the answer the client read", "gateway heap samples, answer sizes in the client request log"}},
	{GatewayLiveHeapReturns, PromiseDescription{"Gateway live heap after the drain returns to its level before the load, within the allowance", "gateway Go runtime metrics (next_gc, stack_inuse) at the phase boundaries"}},
	{ClientErrorAnswered4xx, PromiseDescription{"A client error gets a 4xx, not a 5xx", "mock ML log (what the engine answered) against the status the client got"}},
	{ChargeOnlyTheGuilty, PromiseDescription{"An honest executor is not charged Missed or Invalid, and an answer with forged logprobs is not voted valid. An honest executor that answered in full, whose host a fault of the runner kept from serving its payload with the logprobs when a validator came for it, is voted invalid and loses the payment by design (#1647, #2002); a miss of an honest executor whose host such a fault stopped between the inference's start and its timeout deadline is the protocol's too; those are counted apart", "mock ML log against misses, invalidations and the votes of both phases in the diffs; the runner's host faults that stop a host serving its payload (a pause, a container kill or restart, the public port down) and the validators' payload misses in compose.log"}},
	{HostsConverge, PromiseDescription{"All hosts of an escrow converge to one nonce and one post-state root", "each host's diff chain"}},
	{NodeLocksReleased, PromiseDescription{"Every ML-node lock a host takes is released", "mock-dapi acquire/release journal in compose.log"}},
	{OneNoncePerRequest, PromiseDescription{"A request spends one escrow nonce: nonces burned without sending, per host and minute, against requests", "gateway log in compose.log, client request log"}},
	{QuarantineOnlyWhenFaulted, PromiseDescription{"An honest participant is not quarantined outside its fault windows", "gateway quarantine lines and attempt log in compose.log, host fault windows"}},
}

// promiseExamples is how many violations of one check the report shows.
const promiseExamples = 5

// PromiseResult is a check's outcome in one word.
func PromiseResult(rep PromiseReport, name string) string {
	m, ok := rep.Checked[name]
	switch {
	case !ok:
		return "absent"
	case countViolations(rep, name) > 0:
		return "VIOLATED"
	case m.Measured:
		return "held"
	case strings.HasPrefix(m.Note, "not measured: no allowance:"), !strings.HasPrefix(m.Note, "not measured:"):
		// Counted, but no bound was set to judge it against.
		return "observed"
	default:
		return "not measured"
	}
}

func countViolations(rep PromiseReport, name string) int {
	n := 0
	for _, v := range rep.Violations {
		if v.Promise == name {
			n++
		}
	}
	return n
}

// WritePromiseReport writes a run's checks as Markdown a reader can follow without the code: what
// each check promises, its verdict, what it judged, and the first violations with where they are.
func WritePromiseReport(w io.Writer, title string, rep PromiseReport) error {
	var b strings.Builder
	violated, held, unmeasured := 0, 0, 0
	for _, c := range PromiseCatalog {
		switch PromiseResult(rep, c.Name) {
		case "VIOLATED":
			violated++
		case "held":
			held++
		default:
			unmeasured++
		}
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	if len(rep.Invalid) > 0 {
		fmt.Fprintf(&b, "**Run not valid:** %s. The checks below do not judge the system.\n\n", mdLine(strings.Join(rep.Invalid, "; ")))
	}
	fmt.Fprintf(&b, "%d checks violated, %d held, %d without a verdict (not measured on this run, or no allowance set).\n\n", violated, held, unmeasured)
	b.WriteString("| Check | Promise | Result | Violations | What was judged |\n|---|---|---|---|---|\n")
	for _, c := range PromiseCatalog {
		m := rep.Checked[c.Name]
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %s |\n", c.Name, c.Promise, PromiseResult(rep, c.Name), countViolations(rep, c.Name), mdCell(m.Note))
	}
	b.WriteString("\n## Violations\n")
	listed := false
	for _, c := range PromiseCatalog {
		var got []PromiseViolation
		for _, v := range rep.Violations {
			if v.Promise == c.Name {
				got = append(got, v)
			}
		}
		if len(got) == 0 {
			continue
		}
		listed = true
		fmt.Fprintf(&b, "\n### %s — %d\n\n%s. Reads: %s.\n\n", c.Name, len(got), c.Promise, c.Reads)
		for i, v := range got {
			if i == promiseExamples {
				fmt.Fprintf(&b, "- … %d more in the JSON report\n", len(got)-promiseExamples)
				break
			}
			fmt.Fprintf(&b, "- %s%s\n", violationPlace(v), mdLine(v.Detail))
		}
	}
	if !listed {
		b.WriteString("\nNone.\n")
	}
	if len(rep.ChargedWhileStopped) > 0 {
		fmt.Fprintf(&b, "\n## Invalidated after an honest execution: the executor was stopped before validation\n\n%d inferences an honest executor answered in full; "+
			"when the validators came for its payload (the prompt and the response with its logprobs), a fault of the runner kept its host from serving it "+
			"(a pause, a container kill or restart, the public port down). The validators that could not fetch it voted the result invalid, "+
			"and the executor lost the payment. "+
			"The protocol does this by design (#1647, #2002: a validator votes false when the executor withholds the payload "+
			"or its payload Attach never completes); %s counts them apart, not as violations.\n\n", len(rep.ChargedWhileStopped), ChargeOnlyTheGuilty)
		for _, v := range rep.ChargedWhileStopped {
			fmt.Fprintf(&b, "- %s%s\n", violationPlace(v), mdLine(v.Detail))
		}
	}
	if len(rep.MissedWhileStopped) > 0 {
		fmt.Fprintf(&b, "\n## Missed because a fault of the runner stopped the executor's host before it could finish\n\n%d inferences an honest executor took; "+
			"between the inference's start and its timeout deadline a fault of the runner stopped its host from serving, "+
			"so it could not finish the stream in time. The gateway timed the inference out and the votes charged the miss, "+
			"as the protocol does with any executor that does not finish in time; %s counts them apart, not as violations.\n\n", len(rep.MissedWhileStopped), ChargeOnlyTheGuilty)
		for _, v := range rep.MissedWhileStopped {
			fmt.Fprintf(&b, "- %s%s\n", violationPlace(v), mdLine(v.Detail))
		}
	}
	if len(rep.CaughtOverclaims) > 0 {
		fmt.Fprintf(&b, "\n## Overclaims caught: invalidated, not paid\n\n%d inferences whose Finish claimed other than the ML produced, "+
			"or more than the Start's max_tokens, and which the validators' votes or a usage challenge then invalidated: "+
			"the invalidation took the pay back to the escrow. %s counts them apart, not as violations.\n\n", len(rep.CaughtOverclaims), PaidOutputWithinProduced)
		for _, v := range rep.CaughtOverclaims {
			fmt.Fprintf(&b, "- %s%s\n", violationPlace(v), mdLine(v.Detail))
		}
	}
	if w := rep.Wave; w != nil {
		b.WriteString("\n## Challenge wave\n\nWhat the gateway logged of the usage challenges.\n\n")
		fmt.Fprintf(&b, "| Outcome | Challenges |\n|---|---|\n%s", countRows(w.Outcomes))
		fmt.Fprintf(&b, "\n| Reason | Challenges |\n|---|---|\n%s", countRows(w.Reasons))
		asks := w.Asks()
		fmt.Fprintf(&b, "\n%d voter answers: %d votes, %d not awaited, %d failed (%d still busy with 429 or 503 at the deadline)",
			asks, w.Votes, w.NotAwaited, w.VoteFailed, w.VoteBusy)
		if late := w.Rejected["late"]; asks > 0 {
			fmt.Fprintf(&b, ", %d late (%.1f%%)", late, 100*float64(late)/float64(asks))
		}
		b.WriteString(".\n")
		if len(w.Rejected) > 0 {
			fmt.Fprintf(&b, "\n| Reject cause | Voters |\n|---|---|\n%s", countRows(w.Rejected))
		}
		if d := w.TriggerToVerdictMs; len(d) > 0 {
			fmt.Fprintf(&b, "\nFrom the challenge's start to its outcome, %d challenges: p50 %d ms, p95 %d ms, max %d ms.\n",
				len(d), d[(len(d)-1)/2], d[(len(d)-1)*95/100], d[len(d)-1])
		}
	}
	if len(rep.Observations) > 0 {
		b.WriteString("\n## Observations\n\nReported without a verdict.\n\n")
		for _, o := range rep.Observations {
			fmt.Fprintf(&b, "- %s\n", mdLine(o))
		}
	}
	if len(rep.Open) > 0 {
		fmt.Fprintf(&b, "\n## Open at the export\n\n%d inferences were still Pending or Started with their deadline not yet passed; allowed, listed so a short run does not hide them (all of them in report.json).\n\n", len(rep.Open))
		for _, o := range rep.Open[:min(len(rep.Open), promiseExamples)] {
			fmt.Fprintf(&b, "- %s: escrow %s, inference %d\n", mdLine(o.Host), mdLine(o.Escrow), o.Inference)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// NamedPromiseReport is one run's report under the name the summary shows.
type NamedPromiseReport struct {
	Name   string
	Report PromiseReport
}

// WritePromiseSummary writes one table over several runs: a row per run in name order, a column per
// check. A run that is not valid judged nothing: its row says so and shows no verdict.
func WritePromiseSummary(w io.Writer, runs []NamedPromiseReport) error {
	runs = slices.Clone(runs)
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].Name < runs[j].Name })
	var b strings.Builder
	b.WriteString("# System promise checks over the runs\n\n")
	b.WriteString("A cell is the number of violations, `ok` when the check held, `obs` when it was counted without an allowance, `—` when the run could not measure it or is not valid.\n\n")
	b.WriteString("| Run |")
	for _, c := range PromiseCatalog {
		fmt.Fprintf(&b, " %s |", c.Name)
	}
	b.WriteString("\n|---|")
	for range PromiseCatalog {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, r := range runs {
		valid := len(r.Report.Invalid) == 0
		name := mdCell(r.Name)
		if !valid {
			name += " (not valid)"
		}
		fmt.Fprintf(&b, "| %s |", name)
		for _, c := range PromiseCatalog {
			cell := "—"
			switch result := PromiseResult(r.Report, c.Name); {
			case !valid:
			case result == "VIOLATED":
				cell = fmt.Sprintf("**%d**", countViolations(r.Report, c.Name))
			case result == "held":
				cell = "ok"
			case result == "observed":
				cell = "obs"
			}
			fmt.Fprintf(&b, " %s |", cell)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n| Check | Promise |\n|---|---|\n")
	for _, c := range PromiseCatalog {
		fmt.Fprintf(&b, "| %s | %s |\n", c.Name, c.Promise)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func violationPlace(v PromiseViolation) string {
	var parts []string
	if v.Host != "" {
		parts = append(parts, "host "+v.Host)
	}
	if v.Node != "" {
		parts = append(parts, "ML node "+v.Node)
	}
	if v.Inference != nil {
		parts = append(parts, fmt.Sprintf("escrow %s inference %d", v.Inference.Escrow, v.Inference.Inference))
	}
	if v.RequestID != "" {
		parts = append(parts, "request "+v.RequestID)
	}
	if v.Known != "" {
		parts = append(parts, "known: "+v.Known)
	}
	if len(parts) == 0 {
		return ""
	}
	return "`" + strings.Join(parts, ", ") + "`: "
}

func mdLine(s string) string { return strings.ReplaceAll(s, "\n", " ") }

func mdCell(s string) string { return strings.ReplaceAll(mdLine(s), "|", "\\|") }

// countRows writes a count table's rows, largest first.
func countRows(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "| %s | %d |\n", mdCell(k), counts[k])
	}
	return b.String()
}
