package loadtest

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
)

// ConsoleReport renders saved results for a terminal. WriteReport owns the
// complete Markdown artifact; console output highlights checks needing attention.
func ConsoleReport(outputDir string) (string, error) {
	r, err := loadRunReport(outputDir)
	if err != nil {
		return "", err
	}
	return r.renderConsole(outputDir), nil
}

func consoleSection(out *strings.Builder, title string) {
	fmt.Fprintf(out, "\n%s\n%s\n", title, strings.Repeat("-", len(title)))
}

func consoleLine(out *strings.Builder, label, value string) {
	// Wrap long check details without truncating evidence or flooding the
	// terminal with one unbroken line. Long individual tokens stay intact.
	indent := "  "
	if label != "" {
		indent += label + ": "
	}
	line := indent
	for _, word := range strings.Fields(value) {
		if len(line)+len(word)+1 > 100 && line != indent {
			out.WriteString(strings.TrimRight(line, " ") + "\n")
			indent = "    "
			line = indent
		}
		if line != indent {
			line += " "
		}
		line += word
	}
	out.WriteString(strings.TrimRight(line, " ") + "\n")
}

func consoleTable(out *strings.Builder, rows [][]string) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, row := range rows {
		fmt.Fprintln(w, "  "+strings.Join(row, "\t"))
	}
	_ = w.Flush()
}

func consoleDuration(d time.Duration) string { return d.Round(time.Millisecond).String() }

func (r runReport) renderConsole(outputDir string) string {
	var out strings.Builder
	verdict := "PASS"
	if !r.passed {
		verdict = "FAIL"
	}
	fmt.Fprintf(&out, "\nLoad test: %s (seed %d)\nResult: %s\n", r.summary.Scenario, r.summary.Seed, verdict)
	counts := map[string]int{}
	for _, check := range r.checks {
		counts[reportStatus(check)]++
	}
	var statuses []string
	for _, status := range reportStatusOrder {
		if counts[status] > 0 {
			statuses = append(statuses, fmt.Sprintf("%s %d", status, counts[status]))
		}
	}
	consoleLine(&out, "Checks", strings.Join(statuses, ", "))
	if r.health != nil {
		consoleLine(&out, "Harness", fmt.Sprintf("stalled %s; %d stalls", formatInvalidFraction(r.health.InvalidFraction), len(r.health.Stalls)))
	}
	if r.build != nil {
		consoleLine(&out, "Build", fmt.Sprintf("%s; protocol %s; binary %s", r.build.Name, r.build.Protocol, r.build.BinaryVersion))
	}

	consoleSection(&out, "Client traffic")
	sent := r.summary.Requests - r.summary.Dropped
	consoleTable(&out, [][]string{
		{"Planned / sent / completed", fmt.Sprintf("%d / %d / %d", r.summary.Requests, sent, r.summary.Completed)},
		{"Gateway errors", shareOf(sent-r.summary.Completed, sent)},
		{"Generator drops", shareOf(r.summary.Dropped, r.summary.Requests)},
		{"Generator behind", fmt.Sprint(r.summary.Behind)},
		{"Latency p50 / p95", consoleDuration(r.summary.P50) + " / " + consoleDuration(r.summary.P95)},
		{"Elapsed (including responses)", consoleDuration(r.summary.Duration)},
	})
	for _, name := range []string{"quiet_error_rate", "quiet_latency"} {
		if check, ok := r.check("invariants.error-rate-and-latency." + name); ok {
			consoleLine(&out, strings.ReplaceAll(name, "_", " "), check.Actual+" ["+reportStatus(check)+"]")
		}
	}
	r.consoleLadder(&out)
	if len(r.poc) > 0 {
		consoleSection(&out, "PoC load windows")
		rows := [][]string{{"Window", "OK RPS", "Errors", "Drops", "p95", "p99", "Nonce/OK*", "Burns"}}
		for _, w := range r.poc {
			ratio := reportMissing
			if w.NoncesPerCompletion != nil {
				ratio = fmt.Sprintf("%.2f", *w.NoncesPerCompletion)
			}
			rows = append(rows, []string{w.Window, fmt.Sprintf("%.2f", w.RPS), fmt.Sprint(w.Errors), fmt.Sprint(w.Dropped), consoleDuration(w.P95), consoleDuration(w.P99), ratio, fmt.Sprint(w.Burns)})
		}
		consoleTable(&out, rows)
		consoleLine(&out, "Note", "OK RPS groups eventual completions by request start. *Nonce/OK is approximate; counters cover sampled spans. Burns are nonces committed without sending.")
		for _, w := range r.poc {
			fmt.Fprintf(&out, "\n  %s participants\n", w.Window)
			rows = [][]string{{"Participant (suffix)", "Preserved", "Assigned*", "Misses*", "Host missed*", "ML*", "Aborted*"}}
			for _, p := range w.Participants {
				label := p.Participant
				if len(label) > 12 {
					label = "..." + label[len(label)-12:]
				}
				rows = append(rows, []string{label, fmt.Sprint(p.Preserved), pocUint(p.Assigned), pocUint(p.Misses), pocUint(p.HostMissed), pocUint(p.MLRequests), pocUint(p.MLAborted)})
			}
			consoleTable(&out, rows)
		}
		consoleLine(&out, "Note", "*Participant counters are sampled deltas; unavailable/reset counters are shown as —. Full addresses and timestamps are in report.md and invariants.json.")
	}
	if len(r.events) > 0 {
		consoleSection(&out, "Fault windows")
		for _, event := range r.events {
			if isBackground(event.Params) {
				continue
			}
			w := r.window(event.ID)
			fmt.Fprintf(&out, "\n  %s\n", event.ID)
			consoleLine(&out, "Traffic", fmt.Sprintf("requests=%d drops=%d errors=%d (%.2f%%); non-503/429=%d", w.Requests, w.Dropped, w.Errors, w.ErrorRate*100, w.NonRetryable))
			for _, kind := range []string{"error-rate-and-latency.", "recovery-after-fault."} {
				suffix := ".error_rate"
				label := "Errors"
				if kind == "recovery-after-fault." {
					suffix = ".recovery"
					label = "Recovery"
				}
				if check, ok := r.check("invariants." + kind + event.ID + suffix); ok {
					consoleLine(&out, label, check.Actual+" ["+reportStatus(check)+"]")
				}
			}
		}
	}
	var overlaps [][]string
	for _, w := range r.windows {
		if strings.Contains(w.Window, "+") {
			overlaps = append(overlaps, []string{w.Window, fmt.Sprint(w.Requests), fmt.Sprint(w.Dropped), fmt.Sprint(w.Errors), fmt.Sprintf("%.2f%%", w.ErrorRate*100)})
		}
	}
	if len(overlaps) > 0 {
		consoleSection(&out, "Overlaps (not judged)")
		consoleTable(&out, append([][]string{{"Window", "Requests", "Drops", "Errors", "Rate"}}, overlaps...))
	}
	shown := map[string]bool{}
	consoleSection(&out, "End state")
	for _, check := range r.checks {
		if slices.ContainsFunc(endStatePrefixes, func(prefix string) bool { return strings.HasPrefix(check.Name, prefix) }) {
			consoleLine(&out, "["+reportStatus(check)+"] "+strings.TrimPrefix(check.Name, "invariants."), check.Actual)
			if reportStatus(check) != statusPass {
				consoleCheckDetails(&out, check)
			}
			shown[check.Name] = true
		}
	}
	consoleSection(&out, "Checks needing attention")
	attention := 0
	for _, status := range reportStatusOrder {
		if status == statusPass {
			continue
		}
		for _, check := range r.checks {
			if reportStatus(check) != status || shown[check.Name] {
				continue
			}
			attention++
			fmt.Fprintf(&out, "\n  [%s] %s\n", status, check.Name)
			consoleLine(&out, "Actual", check.Actual)
			consoleCheckDetails(&out, check)
		}
	}
	if attention == 0 {
		consoleLine(&out, "", "None beyond end-state checks above.")
	}
	consoleSection(&out, "Artifacts")
	consoleLine(&out, "Directory", outputDir)
	consoleLine(&out, "Full report", filepath.Join(outputDir, reportFileName))
	consoleLine(&out, "All checks", filepath.Join(outputDir, "assertions.json"))
	return out.String()
}

func consoleCheckDetails(out *strings.Builder, check AssertionResult) {
	consoleLine(out, "Expected", check.Expected)
	if check.Case != "" {
		consoleLine(out, "Case", check.Case)
	}
	if check.Details != "" {
		consoleLine(out, "Details", check.Details)
	}
}

func (r runReport) consoleLadder(out *strings.Builder) {
	if len(r.ladder.steps) == 0 {
		return
	}
	consoleSection(out, "Load steps")
	rows := [][]string{{"Step", "RPS", "Window", "Sent", "Errors", "Never sent", "p50", "p95", "CPU cores", "Attempts/req"}}
	for i, s := range r.ladder.steps {
		cpu, attempts := reportMissing, reportMissing
		if s.HasCPU {
			cpu = fmt.Sprintf("%.2f", s.GatewayCores)
		}
		if s.HasAttempts {
			attempts = fmt.Sprintf("%.2f", s.AttemptsPerRequest)
		}
		rows = append(rows, []string{fmt.Sprint(i + 1), fmt.Sprint(s.RPS), fmt.Sprintf("%s-%s", s.From, s.To), fmt.Sprint(s.Sent), shareOf(s.Sent-s.Completed, s.Sent), shareOf(s.Dropped, s.Offered), consoleDuration(s.P50), consoleDuration(s.P95), cpu, attempts})
	}
	consoleTable(out, rows)
	for i, s := range r.ladder.steps {
		if s.Note != "" {
			consoleLine(out, fmt.Sprintf("Step %d", i+1), s.Note)
		}
		var hosts []string
		for _, h := range s.HostChats {
			hosts = append(hosts, fmt.Sprintf("%s %.1f", h.Host, h.PerSecond))
		}
		if len(hosts) > 0 {
			consoleLine(out, fmt.Sprintf("Step %d host Chats/s", i+1), strings.Join(hosts, ", "))
		}
	}
	for _, note := range r.ladder.notes {
		consoleLine(out, "Note", note)
	}
}
