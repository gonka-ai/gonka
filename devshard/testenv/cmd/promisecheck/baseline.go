package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"devshard/testenv/citest/harness"
)

// baselineRun is one scenario run of a baseline directory (make loadtest-baseline): the runner's
// exit code, its client counts and failed checks, and the promise report when the run wrote one.
type baselineRun struct {
	Name         string
	Exit         string // the runner's exit code, "?" when not recorded
	Requests     int
	Failed       int
	RunnerFailed []string // names of the runner's own checks that failed
	RunnerNote   string   // why the runner's checks are not known, when they are not
	Report       *harness.PromiseReport
}

// readBaseline reads every run directory under dir, in name order. A run directory is one with a
// run.yaml or an exit-code file.
func readBaseline(dir string) ([]baselineRun, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var runs []baselineRun
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runDir := filepath.Join(dir, entry.Name())
		if !exists(filepath.Join(runDir, "run.yaml")) && !exists(filepath.Join(runDir, "exit-code")) {
			continue
		}
		run, err := readBaselineRun(entry.Name(), runDir)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("%s holds no run directories", dir)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Name < runs[j].Name })
	return runs, nil
}

func readBaselineRun(name, dir string) (baselineRun, error) {
	run := baselineRun{Name: name, Exit: "?"}
	if raw, err := os.ReadFile(filepath.Join(dir, "exit-code")); err == nil {
		run.Exit = strings.TrimSpace(string(raw))
	}
	var summary struct {
		Requests int `json:"requests"`
		Failed   int `json:"failed"`
	}
	if err := readJSON(filepath.Join(dir, "summary.json"), &summary); err != nil && !errors.Is(err, os.ErrNotExist) {
		return run, err
	}
	run.Requests, run.Failed = summary.Requests, summary.Failed
	var assertions struct {
		Passed bool   `json:"passed"`
		Error  string `json:"error"`
		Checks []struct {
			Name   string `json:"name"`
			Passed bool   `json:"passed"`
		} `json:"checks"`
	}
	switch err := readJSON(filepath.Join(dir, "assertions.json"), &assertions); {
	case errors.Is(err, os.ErrNotExist):
		run.RunnerNote = "no assertions.json"
	case err != nil:
		return run, err
	default:
		for _, check := range assertions.Checks {
			if !check.Passed {
				run.RunnerFailed = append(run.RunnerFailed, check.Name)
			}
		}
		if !assertions.Passed && len(run.RunnerFailed) == 0 {
			run.RunnerNote = "failed: " + assertions.Error
		}
	}
	rep, err := readReport(filepath.Join(dir, "promises.json"))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return run, err
	default:
		run.Report = &rep
	}
	return run, nil
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// runnerCell is the runner's own verdict: "passed", the failed checks, or why it is not known.
func (r baselineRun) runnerCell() string {
	switch {
	case len(r.RunnerFailed) > 0:
		return strings.Join(r.RunnerFailed, ", ")
	case r.RunnerNote != "":
		return r.RunnerNote
	}
	return "passed"
}

// promisesCell is the promise verdict of the run: no report, the violated checks, or held. "held"
// alone means every check held; otherwise it counts the checks that held, were only observed and were
// not measured. A run that is not valid says so first and still names what it violated.
func (r baselineRun) promisesCell() string {
	if r.Report == nil {
		return "no report"
	}
	var violated []string
	held, observed, unmeasured := 0, 0, 0
	for _, c := range harness.PromiseCatalog {
		switch harness.PromiseResult(*r.Report, c.Name) {
		case "VIOLATED":
			violated = append(violated, c.Name)
		case "held":
			held++
		case "observed":
			observed++
		default:
			unmeasured++
		}
	}
	verdict := "held"
	switch {
	case len(violated) > 0:
		verdict = "violated: " + strings.Join(violated, ", ")
	case observed > 0 || unmeasured > 0:
		verdict = fmt.Sprintf("held %d", held)
		if observed > 0 {
			verdict += fmt.Sprintf(", observed %d", observed)
		}
		if unmeasured > 0 {
			verdict += fmt.Sprintf(", not measured %d", unmeasured)
		}
	}
	if len(r.Report.Invalid) > 0 {
		return "not valid; " + verdict
	}
	return verdict
}

// checkCell is one check of the run as the promise summary shows it. On a run that is not valid the
// check keeps its verdict, marked, so a violation it measured still shows and still compares.
func (r baselineRun) checkCell(name string) string {
	if r.Report == nil {
		return "—"
	}
	v := r.checkVerdict(name)
	if v != "—" && len(r.Report.Invalid) > 0 {
		v += " (run not valid)"
	}
	return v
}

func (r baselineRun) checkVerdict(name string) string {
	switch harness.PromiseResult(*r.Report, name) {
	case "VIOLATED":
		n := 0
		for _, v := range r.Report.Violations {
			if v.Promise == name {
				n++
			}
		}
		return strconv.Itoa(n)
	case "held":
		return "ok"
	case "observed":
		return "obs"
	}
	return "—"
}

func cell(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "|", "\\|") }

// writeBaseline prints one baseline: a row per scenario with the runner's exit code, its client
// counts, its failed checks and the promise verdict, then the promise summary over the runs that
// wrote a report.
func writeBaseline(w io.Writer, dir string, runs []baselineRun) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Load-test baseline: %s\n\n", filepath.Base(filepath.Clean(dir)))
	b.WriteString("| Scenario | Exit | Requests | Failed | Runner checks failed | Promises |\n|---|---|---|---|---|---|\n")
	var named []harness.NamedPromiseReport
	for _, r := range runs {
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %s | %s |\n", cell(r.Name), cell(r.Exit), r.Requests, r.Failed, cell(r.runnerCell()), cell(r.promisesCell()))
		if r.Report != nil {
			named = append(named, harness.NamedPromiseReport{Name: r.Name, Report: *r.Report})
		}
	}
	b.WriteString("\n")
	if _, err := io.WriteString(w, b.String()); err != nil {
		return err
	}
	if len(named) == 0 {
		_, err := io.WriteString(w, "No run wrote a promise report.\n")
		return err
	}
	return harness.WritePromiseSummary(w, named)
}

// writeComparison prints two baselines side by side, a row per scenario of either: the exit code, the
// runner's failed checks, the promise verdict and every check, with a changed cell in bold as
// "before → after". Client counts are shown for both and never marked: they move from run to run.
func writeComparison(w io.Writer, beforeDir, afterDir string, before, after []baselineRun) error {
	byName := func(runs []baselineRun) map[string]baselineRun {
		out := make(map[string]baselineRun, len(runs))
		for _, r := range runs {
			out[r.Name] = r
		}
		return out
	}
	b4, af := byName(before), byName(after)
	var names []string
	for name := range b4 {
		names = append(names, name)
	}
	for name := range af {
		if _, ok := b4[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, "# Load-test baselines compared: %s → %s\n\n", filepath.Base(filepath.Clean(beforeDir)), filepath.Base(filepath.Clean(afterDir)))
	b.WriteString("A changed cell is bold, before → after. A check cell is the number of violations, `ok` when it held, `obs` when counted without an allowance, `—` when not measured; on a run that is not valid it is marked `(run not valid)`.\n\n")
	b.WriteString("| Scenario | Exit | Requests | Failed | Runner checks failed | Promises |")
	for _, c := range harness.PromiseCatalog {
		fmt.Fprintf(&b, " %s |", c.Name)
	}
	b.WriteString("\n|---|---|---|---|---|---|")
	for range harness.PromiseCatalog {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	changed := 0
	for _, name := range names {
		x, inBefore := b4[name]
		y, inAfter := af[name]
		pick := func(f func(baselineRun) string) (string, string) {
			left, right := "absent", "absent"
			if inBefore {
				left = f(x)
			}
			if inAfter {
				right = f(y)
			}
			return left, right
		}
		marked := func(f func(baselineRun) string) string {
			left, right := pick(f)
			if left == right {
				return cell(left)
			}
			changed++
			return "**" + cell(left) + " → " + cell(right) + "**"
		}
		plain := func(f func(baselineRun) string) string {
			left, right := pick(f)
			return cell(left) + " / " + cell(right)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |", cell(name),
			marked(func(r baselineRun) string { return r.Exit }),
			plain(func(r baselineRun) string { return strconv.Itoa(r.Requests) }),
			plain(func(r baselineRun) string { return strconv.Itoa(r.Failed) }),
			marked(baselineRun.runnerCell), marked(baselineRun.promisesCell))
		for _, c := range harness.PromiseCatalog {
			fmt.Fprintf(&b, " %s |", marked(func(r baselineRun) string { return r.checkCell(c.Name) }))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\n%d changed cells.\n", changed)
	_, err := io.WriteString(w, b.String())
	return err
}
