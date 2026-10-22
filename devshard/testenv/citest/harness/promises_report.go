package harness

import (
	"fmt"
	"io"
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
	{PaidOutputWithinProduced, PromiseDescription{"Finish.OutputTokens equals the tokens the ML execution produced and never exceeds max_tokens", "MsgStartInference, MsgFinishInference, mock ML log"}},
	{ChargeOnlyTheGuilty, PromiseDescription{"An honest executor is not charged Missed or Invalid, and an answer with forged logprobs is not voted valid", "mock ML log against Missed/Invalid/valid votes in the diffs"}},
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
	_, err := io.WriteString(w, b.String())
	return err
}

func violationPlace(v PromiseViolation) string {
	var parts []string
	if v.Host != "" {
		parts = append(parts, "host "+v.Host)
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
