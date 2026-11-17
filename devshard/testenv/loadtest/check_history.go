package loadtest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

const (
	// knownBugXPassRuns is how many runs in a row a known_bug check must pass before the report
	// says to drop its marker: timeout-vote-fanout's bug reproduced in only 1 of 3 runs of identical code.
	knownBugXPassRuns = 5
	// checkHistoryFile sits beside the run directories by default, so repeats of a scenario share it.
	checkHistoryFile = "check-history.json"
	// repeatRuns is the window a fixOnRepeat failure must fail in twice: this run and the 4 before it.
	repeatRuns = 5
)

// repeatJudged are the checks recorded on every run, so that a fixOnRepeat failure can look back.
var repeatJudged = map[string]bool{"invariants.no-nonce-left-pending.max_pending_age": true}

// historyKey names the runs whose outcomes of a check form one history.
type historyKey struct {
	Scenario string `json:"scenario"`
	Gateway  string `json:"gateway"`
}

// historyRun is one run's outcome of one known_bug or repeatJudged check.
type historyRun struct {
	Run     string    `json:"run"`
	Started time.Time `json:"started"`
	Status  string    `json:"status"` // XPASS or XFAIL; PASS or FAIL, before the repeat rule, for repeatJudged
}

// checkHistory is the JSON file: outcomes by "scenario|gateway|check", one per run start.
type checkHistory struct {
	Checks map[string][]historyRun `json:"checks"`
}

// defaultCheckHistory is the history file beside run directory dir.
func defaultCheckHistory(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return filepath.Join(filepath.Dir(dir), checkHistoryFile)
}

// recordCheckHistory adds this run's XPASS, XFAIL and repeatJudged checks to the history at path,
// replacing an earlier record of the same run start, and returns checks with each XPASS check's
// details naming how many runs in a row, up to this one, it passed, and each fixOnRepeat failure
// passed unless one of the repeatRuns-1 runs before it failed too. One writer at a time: runs on a VM are serial.
func recordCheckHistory(path string, key historyKey, run string, started time.Time, checks []AssertionResult) ([]AssertionResult, error) {
	history := checkHistory{Checks: map[string][]historyRun{}}
	body, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return checks, err
	default:
		if err := json.Unmarshal(body, &history); err != nil {
			return checks, fmt.Errorf("%s: %w", path, err)
		}
		if history.Checks == nil {
			history.Checks = map[string][]historyRun{}
		}
	}
	marked := append([]AssertionResult(nil), checks...)
	changed := false
	for i, check := range marked {
		status := check.Status
		switch {
		case status == "XPASS" || status == "XFAIL":
		case status == "" && repeatJudged[check.Name] && check.Passed:
			status = statusPass
		case status == "" && repeatJudged[check.Name]:
			status = "FAIL"
		default:
			continue
		}
		name := key.Scenario + "|" + key.Gateway + "|" + check.Name
		runs := slices.DeleteFunc(slices.Clone(history.Checks[name]), func(r historyRun) bool { return r.Started.Equal(started) })
		runs = append(runs, historyRun{Run: run, Started: started, Status: status})
		sort.Slice(runs, func(a, b int) bool { return runs[a].Started.Before(runs[b].Started) })
		history.Checks[name], changed = runs, true
		if status == "FAIL" && check.fixOnRepeat {
			marked[i] = judgeRepeat(check, runs, started)
		}
		if status != "XPASS" {
			continue
		}
		inARow := 0
		for j := len(runs) - 1; j >= 0; j-- {
			if runs[j].Started.After(started) {
				continue
			}
			if runs[j].Status != "XPASS" {
				break
			}
			inARow++
		}
		if inARow >= knownBugXPassRuns {
			marked[i].Details = fmt.Sprintf("%s; XPASS %d runs in a row: drop the marker", check.Details, inARow)
		} else {
			marked[i].Details = fmt.Sprintf("%s; XPASS %d runs in a row of the %d needed to drop the marker", check.Details, inARow, knownBugXPassRuns)
		}
	}
	if !changed {
		return marked, nil
	}
	body, err = json.MarshalIndent(history, "", "  ")
	if err != nil {
		return checks, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o644); err != nil {
		return checks, err
	}
	return marked, os.Rename(tmp, path)
}

// judgeRepeat keeps check failed if one of the repeatRuns-1 runs started before it failed too,
// and passes it otherwise; runs is the check's sorted history including this run.
func judgeRepeat(check AssertionResult, runs []historyRun, started time.Time) AssertionResult {
	before := 0
	for j := len(runs) - 1; j >= 0 && before < repeatRuns-1; j-- {
		if !runs[j].Started.Before(started) {
			continue
		}
		before++
		if runs[j].Status == "FAIL" {
			check.Details = fmt.Sprintf("%s; repeats %s within the last %d runs", check.Details, runs[j].Run, repeatRuns)
			return check
		}
	}
	check.Passed = true
	check.Details = fmt.Sprintf("%s; FIX only if it repeats: no record over the bound in the %d runs before", check.Details, repeatRuns-1)
	return check
}
