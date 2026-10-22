// Binary promisecheck holds a finished load-test run to the system properties of the harness's
// CheckPromises and prints the report as JSON. It reads only the run directory the load-test runner
// (testenv/loadtest) wrote: requests.jsonl, run.yaml, compose.log and the export under export/.
//
// Exit status: 0 when the run broke no property, or only ones listed as known; 1 when it broke one
// not listed; 2 when the run could not be read or the report not written; 3 when the run is not valid
// (the load or the dishonest nodes did not do what the checks need), as the runner fails it.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

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
	flag.Parse()
	if *runDir == "" {
		fail("-loadtest is required")
	}
	known, err := readKnown(*knownPath)
	if err != nil {
		fail(err.Error())
	}
	rep, err := harness.CheckLoadtestRun(*runDir, known)
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
