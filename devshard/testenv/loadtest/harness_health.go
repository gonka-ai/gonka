package loadtest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	statusInvalid = "INVALID"
	// statusNotJudged marks a check the run left no data for, such as a
	// fault whose every request another fault also touched.
	statusNotJudged = "NOT_JUDGED"
	// stallFactor: a heartbeat gap above stallFactor x its median period is a
	// harness stall, so checks over that time judge the VM, not devshard.
	stallFactor = 3
	// minStallGap: a 1 s scrape that misses a few beats under load is not a
	// VM stall; only gaps longer than this can be one.
	minStallGap     = 10 * time.Second
	reconcileNeedle = "reconcile desired versions"
)

// harnessStall is one heartbeat gap longer than stallFactor x its period and minStallGap.
type harnessStall struct {
	Source string    `json:"source"`
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Gap    string    `json:"gap"`
	Period string    `json:"period"`
}

// harnessHealth is the invariants.json view of the stalls and what they void.
type harnessHealth struct {
	Stalls          []harnessStall `json:"stalls"`
	InvalidWindows  []timeSpan     `json:"invalid_windows"`
	InvalidFraction float64        `json:"invalid_fraction"`
	InvalidChecks   []string       `json:"invalid_checks,omitempty"`
}

// heartbeatStalls returns the gaps of ticks longer than minStallGap and stallFactor x their
// median gap. Gaps overlapping an excluded span (an injected fault) are skipped.
func heartbeatStalls(source string, ticks []time.Time, excluded []timeSpan) []harnessStall {
	if len(ticks) < 3 {
		return nil
	}
	sorted := append([]time.Time(nil), ticks...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Before(sorted[j]) })
	gaps := make([]time.Duration, 0, len(sorted)-1)
	for i := 1; i < len(sorted); i++ {
		gaps = append(gaps, sorted[i].Sub(sorted[i-1]))
	}
	ordered := append([]time.Duration(nil), gaps...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	period := ordered[len(ordered)/2]
	if period <= 0 {
		return nil
	}
	var stalls []harnessStall
	for i, gap := range gaps {
		if gap <= max(stallFactor*period, minStallGap) {
			continue
		}
		span := timeSpan{From: sorted[i], To: sorted[i+1]}
		if overlapsAny(span, excluded) {
			continue
		}
		stalls = append(stalls, harnessStall{Source: source, From: span.From, To: span.To, Gap: gap.String(), Period: period.String()})
	}
	return stalls
}

func overlapsAny(span timeSpan, windows []timeSpan) bool {
	for _, w := range windows {
		if !span.From.After(w.To) && !w.From.After(span.To) {
			return true
		}
	}
	return false
}

// harnessStalls checks every versiond reconcile ticker and the gateway
// metrics scrape. Only gaps overlapping the workload count.
func (c *invariantContext) harnessStalls() []harnessStall {
	workload := timeSpan{From: c.in.Started, To: c.runEnd}
	injected := map[string][]timeSpan{}
	for _, record := range c.in.Events {
		if record.StartedAt == nil {
			continue
		}
		end := c.runEnd
		if record.EndedAt != nil {
			end = *record.EndedAt
		}
		target := record.Target
		if record.Action == "gateway_restart" {
			target = "gateway"
		} else if eventActions[record.Action].target != targetHost {
			continue
		}
		injected[target] = append(injected[target], timeSpan{From: *record.StartedAt, To: end})
	}
	var stalls []harnessStall
	services := make([]string, 0, len(c.in.ReconcileTicks))
	for service := range c.in.ReconcileTicks {
		services = append(services, service)
	}
	sort.Strings(services)
	for _, service := range services {
		stalls = append(stalls, heartbeatStalls(service+" reconcile tick", c.in.ReconcileTicks[service], injected[service])...)
	}
	var scrapes []time.Time
	for _, s := range c.in.Metrics {
		if s.Target == "gateway" && len(s.Values) > 0 {
			scrapes = append(scrapes, s.At)
		}
	}
	stalls = append(stalls, heartbeatStalls("gateway metrics scrape", scrapes, injected["gateway"])...)
	var out []harnessStall
	for _, stall := range stalls {
		if overlapsAny(timeSpan{From: stall.From, To: stall.To}, []timeSpan{workload}) {
			out = append(out, stall)
		}
	}
	return out
}

// invalidWindows extends each stall by the grace, because nonces started
// before it are voted late, and merges overlapping windows.
func invalidWindows(stalls []harnessStall, grace time.Duration) []timeSpan {
	spans := make([]timeSpan, 0, len(stalls))
	for _, s := range stalls {
		spans = append(spans, timeSpan{From: s.From, To: s.To.Add(grace)})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].From.Before(spans[j].From) })
	var merged []timeSpan
	for _, s := range spans {
		if n := len(merged); n > 0 && !s.From.After(merged[n-1].To) {
			if s.To.After(merged[n-1].To) {
				merged[n-1].To = s.To
			}
			continue
		}
		merged = append(merged, s)
	}
	return merged
}

// invalidFraction is the share of the workload covered by invalid windows.
func invalidFraction(windows []timeSpan, from, to time.Time) float64 {
	total := to.Sub(from)
	if total <= 0 {
		return 0
	}
	var covered time.Duration
	for _, w := range windows {
		start, end := w.From, w.To
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		if end.After(start) {
			covered += end.Sub(start)
		}
	}
	return float64(covered) / float64(total)
}

// markInvalid turns a check INVALID when every window it judged overlaps an
// invalid window, whether it passed or failed: a stalled harness proves nothing.
func markInvalid(checks []AssertionResult, windows []timeSpan) []string {
	var names []string
	for i := range checks {
		check := &checks[i]
		if len(windows) == 0 || len(check.spans) == 0 {
			continue
		}
		all := true
		for _, span := range check.spans {
			if !overlapsAny(span, windows) {
				all = false
				break
			}
		}
		if !all {
			continue
		}
		check.Passed = false
		check.Status = statusInvalid
		check.Details = strings.TrimSuffix("harness stall overlaps every judged window; "+check.Details, "; ")
		names = append(names, check.Name)
	}
	return names
}

func withSpans(check AssertionResult, spans ...timeSpan) AssertionResult {
	check.spans = spans
	return check
}

// readReconcileTicks reads the versiond reconcile log lines of a Compose log,
// keyed by service ("versiond-3-1 | 2026/10/13 06:10:29 INFO reconcile ...").
func readReconcileTicks(path string) (map[string][]time.Time, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	ticks := map[string][]time.Time{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		if service, at, ok := parseReconcileTick(scanner.Text()); ok {
			ticks[service] = append(ticks[service], at)
		}
	}
	return ticks, scanner.Err()
}

func parseReconcileTick(line string) (string, time.Time, bool) {
	prefix, rest, ok := strings.Cut(line, " | ")
	if !ok || !strings.Contains(rest, reconcileNeedle) {
		return "", time.Time{}, false
	}
	service := strings.TrimSpace(prefix)
	if i := strings.LastIndex(service, "-"); i > 0 {
		if _, err := strconv.Atoi(service[i+1:]); err == nil {
			service = service[:i]
		}
	}
	rest = strings.TrimSpace(rest)
	if len(rest) >= 19 {
		if at, err := time.ParseInLocation("2006/01/02 15:04:05", rest[:19], time.UTC); err == nil {
			return service, at, true
		}
	}
	var entry struct {
		Time time.Time `json:"time"`
	}
	if err := json.Unmarshal([]byte(rest), &entry); err == nil && !entry.Time.IsZero() {
		return service, entry.Time.UTC(), true
	}
	return "", time.Time{}, false
}

func formatInvalidFraction(f float64) string {
	return fmt.Sprintf("%.1f%%", f*100)
}
