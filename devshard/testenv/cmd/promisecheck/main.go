// Binary promisecheck holds a finished load-test run to the system properties of the harness's
// CheckPromises and prints the report as JSON. It reads only the run directory the load-test runner
// (testenv/loadtest) wrote: requests.jsonl, run.yaml, compose.log, metrics.jsonl, host-faults.json
// and the export under export/.
//
// Exit status: 0 when the run broke no property, or only ones listed as known; 1 when it broke one
// not listed; 2 when the run could not be read, the report not written or the command line is wrong;
// 3 when the run is not valid (the generator dropped requests, or the export lost what the checks
// read). The runner also fails a run whose dishonest nodes never lied, which the run directory alone
// does not tell, so for such a run this exits 0 where the runner fails.
//
// -render prints the Markdown report of a saved JSON report; -summarize prints one table over
// several saved reports. Neither reads a run, and neither goes with -loadtest or with the other.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"devshard/testenv/citest/harness"
)

type knownEntry struct {
	Promise string `json:"promise"`
	Detail  string `json:"detail"` // regular expression over the violation detail; empty matches all
	Ref     string `json:"ref"`
}

func main() {
	runDir := flag.String("loadtest", "", "output directory of the load-test runner (requests.jsonl, run.yaml, compose.log, export/)")
	knownPath := flag.String("known", "", "JSON list of known violations: [{promise, detail, ref}]")
	reportPath := flag.String("report", "", "also write the report as Markdown to this file")
	vllmErrors := flag.Bool("mock-errors-vllm", false, "the mock wrote its error bodies the way vLLM does; without it a client_error_answered_4xx mismatch is an observation and the check gives no verdict")
	allowance := flag.Uint64("live-heap-allowance", 0, "bytes the gateway's live heap may grow over the load and drain; 0 reports without a verdict")
	perInference := flag.Uint64("live-heap-per-inference", 0, "bytes the live heap check also allows for each inference of an escrow not settled by the export; 0 keeps the plain allowance")
	lockGrace := flag.Duration("lock-grace", 3*time.Minute, "a lock taken this close to the end of the mock-dapi journal may still be released; 0 also means 3m")
	burnAllowance := flag.Float64("burn-allowance", 0, "nonces burned without sending per request one minute may reach on a host; 0 reports without a verdict")
	heapPerByte := flag.Float64("heap-per-response-byte", 0, "the gateway heap growth per request gateway_heap_bounded allows, in bytes per byte of the answer; 0 reports without a verdict")
	summarize := flag.Bool("summarize", false, "read the JSON reports named as arguments (name=path or path) and print one Markdown table over them; no -loadtest")
	render := flag.String("render", "", "print the Markdown report of a saved JSON report and exit; no -loadtest")
	flag.Parse()
	switch {
	case *render != "" && *summarize:
		fail("-render and -summarize do not go together")
	case (*render != "" || *summarize) && *runDir != "":
		fail("-render and -summarize read saved reports, not a run: drop -loadtest")
	case *summarize && flag.NArg() == 0:
		fail("-summarize needs the saved reports to read")
	case !(*burnAllowance >= 0):
		fail("-burn-allowance must be a number of 0 or more")
	case !(*heapPerByte >= 0):
		fail("-heap-per-response-byte must be a number of 0 or more")
	case *lockGrace < 0:
		fail("-lock-grace must not be negative")
	}
	if *render != "" {
		rep, err := readReport(*render)
		if err != nil {
			fail(err.Error())
		}
		if err := harness.WritePromiseReport(os.Stdout, "System promise checks: "+filepath.Base(filepath.Dir(*render)), rep); err != nil {
			fail(err.Error())
		}
		return
	}
	if *summarize {
		if err := writeSummary(flag.Args()); err != nil {
			fail(err.Error())
		}
		return
	}
	if *runDir == "" {
		fail("-loadtest is required")
	}
	known, err := readKnown(*knownPath)
	if err != nil {
		fail(err.Error())
	}
	rep, err := harness.CheckLoadtestRunWithin(*runDir, harness.PromiseInput{Known: known, MockErrorsVLLMShaped: *vllmErrors,
		LiveHeapAllowance: *allowance, LiveHeapPerInference: *perInference, LockGrace: *lockGrace, BurnAllowance: *burnAllowance,
		HeapPerResponseByte: *heapPerByte})
	if err != nil {
		fail(err.Error())
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		fail(err.Error())
	}
	if *reportPath != "" {
		f, err := os.Create(*reportPath)
		if err != nil {
			fail(err.Error())
		}
		if err := harness.WritePromiseReport(f, "System promise checks: "+filepath.Base(filepath.Clean(*runDir)), rep); err != nil {
			fail(err.Error())
		}
		if err := f.Close(); err != nil {
			fail(err.Error())
		}
	}
	if len(rep.Invalid) > 0 {
		os.Exit(3)
	}
	if rep.Unknown() > 0 {
		os.Exit(1)
	}
}

// fail reports a run that could not be read or a report that could not be written.
func fail(msg string) {
	fmt.Fprintln(os.Stderr, "promisecheck:", msg)
	os.Exit(2)
}

// writeSummary prints one table over saved JSON reports; an argument is the path of a report, whose
// parent directory names the run, or name=path. An argument that names an existing file is a path,
// even when it holds '='.
func writeSummary(args []string) error {
	var runs []harness.NamedPromiseReport
	for _, arg := range args {
		path, name := arg, filepath.Base(filepath.Dir(arg))
		if _, err := os.Stat(arg); err != nil {
			var ok bool
			if name, path, ok = strings.Cut(arg, "="); !ok {
				path, name = arg, filepath.Base(filepath.Dir(arg))
			}
		}
		rep, err := readReport(path)
		if err != nil {
			return err
		}
		runs = append(runs, harness.NamedPromiseReport{Name: name, Report: rep})
	}
	return harness.WritePromiseSummary(os.Stdout, runs)
}

func readReport(path string) (harness.PromiseReport, error) {
	var rep harness.PromiseReport
	raw, err := os.ReadFile(path)
	if err != nil {
		return rep, err
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		return rep, fmt.Errorf("%s: %w", path, err)
	}
	return rep, nil
}

func readKnown(path string) ([]harness.KnownViolation, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []knownEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []harness.KnownViolation
	for _, e := range entries {
		k := harness.KnownViolation{Promise: e.Promise, Ref: e.Ref}
		if e.Detail != "" {
			if k.Detail, err = regexp.Compile(e.Detail); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		}
		out = append(out, k)
	}
	return out, nil
}
