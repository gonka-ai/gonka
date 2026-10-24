package harness

// Readers for the run directory the load-test runner (testenv/loadtest) writes. Its requests.jsonl
// names no inference: each line is one client request with its HTTP status and outcome. The
// generator tags the last message of every request with " [load-request:<request_id>]" (unless the
// run replays a dataset), so the prompt key of each request can be rebuilt from the scenario's
// request in run.yaml and found in the mock log; the mock log then names the seeds, and the diffs
// name the inference that ran with that model, seed and time.
//
// The files are decoded here with their own types: the runner imports this package, so this
// package cannot import the runner's.

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"devshard/testenv/mockopenai"
)

// loadRequestTag is what the generator appends to the last message of a request
// (executeRequest in testenv/loadtest/generator.go).
const loadRequestTag = " [load-request:%s]"

// LoadtestLinkReport counts how the client requests of a load-test run were tied to inferences.
type LoadtestLinkReport struct {
	Requests      int  `json:"requests"`       // requests the generator sent
	Keyed         int  `json:"keyed"`          // with a prompt key found in the mock log
	Executions    int  `json:"executions"`     // execute records of the keyed requests
	Linked        int  `json:"linked"`         // executions tied to exactly one inference
	LinkedNearest int  `json:"linked_nearest"` // of those, tied to the closest start among several
	LinkedPayload int  `json:"linked_payload"` // of those, tied through the prompt a host stored for the inference
	NoInference   int  `json:"no_inference"`   // no started inference of that model and seed in the window
	AmbiguousExec int  `json:"ambiguous_exec"` // several inferences of that model and seed in the window
	Replayed      bool `json:"replayed"`       // requests replayed from a dataset: no request tags
	Dropped       int  `json:"dropped"`        // requests the generator dropped at max_in_flight
}

// loadtestRunYAML is the part of the scenario that names the request the generator sends. The
// generator sends one fixed request per scenario; a scenario that drew its prompts from several
// would need each of them here to rebuild the prompt keys.
type loadtestRunYAML struct {
	Workload struct {
		Request struct {
			Model    string                   `yaml:"model"`
			Messages []mockopenai.ChatMessage `yaml:"messages"`
		} `yaml:"request"`
	} `yaml:"workload"`
}

// loadtestRequestLine is one line of requests.jsonl (RequestResult in testenv/loadtest/generator.go).
type loadtestRequestLine struct {
	RequestID   string `json:"request_id"`
	SampleIndex *int   `json:"sample_index"`
	Status      int    `json:"status"`
	Outcome     string `json:"outcome"`
}

// IsLoadtestRun says whether a run directory was written by the load-test runner: it keeps the
// scenario as run.yaml next to requests.jsonl.
func IsLoadtestRun(runDir string) bool {
	_, errScenario := os.Stat(filepath.Join(runDir, "run.yaml"))
	_, errRequests := os.Stat(filepath.Join(runDir, "requests.jsonl"))
	return errScenario == nil && errRequests == nil
}

// ReadLoadtestRequests reads the client log of a load-test run and ties each request to the
// inferences that served it, through the mock log (prompt key, model, seed) and the diffs (model,
// inference id, start time). executors names the host that executed each inference
// (ReadExecutors). Requests the generator never sent are left out. A 200 the generator did not
// accept as an answer is kept with Incomplete set.
func ReadLoadtestRequests(runDir string, mock []mockopenai.RequestRecord, diffs ExportDiffs, executors map[InferenceRef]string) ([]ClientRequest, LoadtestLinkReport, error) {
	var rep LoadtestLinkReport
	raw, err := os.ReadFile(filepath.Join(runDir, "run.yaml"))
	if err != nil {
		return nil, rep, err
	}
	var scenario loadtestRunYAML
	if err := yaml.Unmarshal(raw, &scenario); err != nil {
		return nil, rep, fmt.Errorf("run.yaml: %w", err)
	}
	model, prompt := scenario.Workload.Request.Model, scenario.Workload.Request.Messages

	stored, err := readStoredPrompts(filepath.Join(runDir, "export"))
	if err != nil {
		return nil, rep, err
	}

	execs := map[string][]mockopenai.RequestRecord{}
	for _, m := range mock {
		if m.Phase == mockopenai.PhaseExecute {
			execs[m.PromptKey] = append(execs[m.PromptKey], m)
		}
	}
	type modelSeed struct {
		model string
		id    uint64
	}
	started := map[modelSeed][]InferenceRef{}
	startedAt := map[InferenceRef]int64{}
	for _, c := range diffs.Hosts {
		for ref, f := range c.Inferences {
			if !f.Started {
				continue
			}
			if _, seen := startedAt[ref]; seen {
				continue
			}
			startedAt[ref] = f.StartedAt
			k := modelSeed{f.Model, ref.Inference}
			started[k] = append(started[k], ref)
		}
	}

	var out []ClientRequest
	err = readJSONLines(filepath.Join(runDir, "requests.jsonl"), func(line []byte) error {
		var r loadtestRequestLine
		if err := json.Unmarshal(line, &r); err != nil {
			return err
		}
		if r.Outcome == "dropped_by_generator" {
			rep.Dropped++
			return nil
		}
		rep.Requests++
		// The generator records "error" for a 200 it did not accept as an answer, and the scenario's
		// terminal outcome for one it did.
		cr := ClientRequest{RequestID: r.RequestID, HTTPStatus: r.Status,
			Incomplete: r.Status == 200 && r.Outcome == "error"}
		if r.SampleIndex != nil {
			rep.Replayed = true
			out = append(out, cr)
			return nil
		}
		key := ""
		if len(prompt) > 0 {
			msgs := slices.Clone(prompt)
			msgs[len(msgs)-1].Content += fmt.Sprintf(loadRequestTag, r.RequestID)
			body, err := json.Marshal(struct {
				Model    string                   `json:"model"`
				Messages []mockopenai.ChatMessage `json:"messages"`
			}{model, msgs})
			if err != nil {
				return err
			}
			if k := mockopenai.PromptKey(body); len(execs[k]) > 0 {
				key = k
			}
		}
		if key != "" {
			rep.Keyed++
			cr.PromptKey = key
			for _, m := range execs[key] {
				rep.Executions++
				if m.Seed == nil || *m.Seed < 0 {
					rep.NoInference++
					continue
				}
				// A host stores the prompt of every inference it holds, request tag included, so the
				// request names its inferences on every escrow; the seed of an execution is its
				// inference id.
				var hits []InferenceRef
				for _, ref := range stored[r.RequestID] {
					if ref.Inference == uint64(*m.Seed) {
						hits = append(hits, ref)
					}
				}
				if len(hits) == 1 {
					rep.Linked++
					rep.LinkedPayload++
					cr.Executions = append(cr.Executions, Execution{InferenceRef: hits[0],
						Participant: executors[hits[0]], Instance: m.Node, Outcome: m.Outcome})
					continue
				}
				hits = nil
				for _, ref := range started[modelSeed{m.Model, uint64(*m.Seed)}] {
					at := startedAt[ref] * 1000
					if m.StartUnixMs >= at-MatchWindowBeforeMs && m.StartUnixMs <= at+MatchWindowAfterMs {
						hits = append(hits, ref)
					}
				}
				if len(hits) > 1 {
					if ref, ok := nearestStart(hits, startedAt, m.StartUnixMs); ok {
						hits = []InferenceRef{ref}
						rep.LinkedNearest++
					}
				}
				switch len(hits) {
				case 0:
					rep.NoInference++
				case 1:
					rep.Linked++
					cr.Executions = append(cr.Executions, Execution{InferenceRef: hits[0],
						Participant: executors[hits[0]], Instance: m.Node, Outcome: m.Outcome})
				default:
					rep.AmbiguousExec++
				}
			}
		}
		out = append(out, cr)
		return nil
	})
	if err != nil {
		return nil, rep, err
	}
	return out, rep, nil
}

// readStoredPrompts maps each load-test request id to the inferences whose prompt a host stored
// (payload_storage of every exported Postgres database). An escrow's inference ids restart at 1, so
// with several escrows of one model the seed of an execution names an inference on each of them; the
// stored prompt carries the request tag and says which one ran. Only the prompt column is kept.
func readStoredPrompts(exportDir string) (map[string][]InferenceRef, error) {
	files, _ := filepath.Glob(filepath.Join(exportDir, "postgres", "public.payload_storage.csv"))
	more, _ := filepath.Glob(filepath.Join(exportDir, "db-*", "postgres", "public.payload_storage.csv"))
	out := map[string][]InferenceRef{}
	seen := map[InferenceRef]bool{}
	for _, path := range append(files, more...) {
		if err := readPromptRows(path, func(ref InferenceRef, prompt []byte) {
			_, rest, ok := bytes.Cut(prompt, []byte("[load-request:"))
			if !ok {
				return
			}
			id, _, ok := bytes.Cut(rest, []byte("]"))
			if !ok || seen[ref] {
				return
			}
			seen[ref] = true
			out[string(id)] = append(out[string(id)], ref)
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// readPromptRows streams a payload_storage CSV and hands each row's escrow, inference id and decoded
// prompt to each.
func readPromptRows(path string, each func(InferenceRef, []byte)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := csv.NewReader(bufio.NewReaderSize(f, 1<<20))
	r.ReuseRecord = true
	head, err := r.Read()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[h] = i
	}
	escrowCol, okE := col["escrow_id"]
	idCol, okI := col["inference_id"]
	promptCol, okP := col["prompt_payload"]
	if !okE || !okI || !okP {
		return nil
	}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		id, err := strconv.ParseUint(rec[idCol], 10, 64)
		if err != nil {
			continue
		}
		prompt, err := hex.DecodeString(strings.TrimPrefix(rec[promptCol], `\x`))
		if err != nil {
			continue
		}
		each(InferenceRef{Escrow: rec[escrowCol], Inference: id}, prompt)
	}
}

// nearestStartMarginMs is how much farther the second-closest inference start must be before an
// execution is tied to the closest one. Two escrows of one model reuse inference ids within the
// match window; on a 15-minute run at steady load the closest start was at most 23 s away and the
// second at least 51 s.
const nearestStartMarginMs = 5000

// nearestStart picks the inference whose start is closest to an execution's start, when the next
// closest is at least nearestStartMarginMs farther; otherwise the execution stays ambiguous.
func nearestStart(hits []InferenceRef, startedAt map[InferenceRef]int64, execMs int64) (InferenceRef, bool) {
	dist := func(ref InferenceRef) int64 {
		d := execMs - startedAt[ref]*1000
		if d < 0 {
			d = -d
		}
		return d
	}
	sorted := slices.Clone(hits)
	slices.SortFunc(sorted, func(a, b InferenceRef) int { return cmp.Compare(dist(a), dist(b)) })
	if dist(sorted[1])-dist(sorted[0]) < nearestStartMarginMs {
		return InferenceRef{}, false
	}
	return sorted[0], true
}

// CheckLoadtestRun holds a finished load-test run to the checks: the runner's requests.jsonl and
// run.yaml and compose.log in runDir, and the export the runner took under runDir/export. An error
// means the export could not be read.
func CheckLoadtestRun(runDir string, known []KnownViolation) (PromiseReport, error) {
	exportDir := filepath.Join(runDir, "export")
	in := PromiseInput{Known: known}
	var err error
	if in.Diffs, err = ReadExportDiffs(exportDir); err != nil {
		return PromiseReport{}, err
	}
	var observations []string
	complete := false
	in.Mock, complete, err = ReadMockLog(exportDir)
	mockRead := err == nil
	switch {
	case os.IsNotExist(err):
		observations = append(observations, "the export has no mock-log.json: output tokens are judged against max_tokens only")
	case err != nil:
		return PromiseReport{}, err
	}
	executors, err := ReadExecutors(filepath.Join(runDir, "compose.log"))
	switch {
	case os.IsNotExist(err):
		observations = append(observations, "the run kept no compose.log: executors are not named")
	case err != nil:
		return PromiseReport{}, err
	}
	var link LoadtestLinkReport
	if in.Requests, link, err = ReadLoadtestRequests(runDir, in.Mock, in.Diffs, executors); err != nil {
		return PromiseReport{}, err
	}
	rep := CheckPromises(in)
	// A lost chain or mock log reads as rows that never happened: the checks cannot tell it from a
	// property held. Other tables (a payload_storage over the file bound) the checks do not read.
	switch m, err := readExportManifest(exportDir); {
	case os.IsNotExist(err):
		observations = append(observations, "the export has no manifest: whether it is whole is not known")
	case err != nil:
		return PromiseReport{}, fmt.Errorf("export manifest: %w", err)
	case !m.Complete:
		judged, other := exportGaps(m)
		if len(judged) > 0 {
			rep.Invalid = append(rep.Invalid, "the export lost what the checks read: "+strings.Join(judged, "; "))
		}
		if len(other) > 0 {
			observations = append(observations, "the export is not whole outside what the checks read: "+strings.Join(other, "; "))
		}
	}
	if mockRead && !complete && len(in.Mock) == 0 {
		rep.Invalid = append(rep.Invalid, "the mock log is incomplete and holds no record: no execution can be judged")
	}
	if link.Dropped > 0 {
		rep.Invalid = append(rep.Invalid, fmt.Sprintf(
			"the generator dropped %d of %d requests at max_in_flight: the load is not the one the scenario asked for",
			link.Dropped, link.Dropped+link.Requests))
	}
	if rep.Wave, err = ReadChallengeWave(filepath.Join(runDir, "compose.log")); err != nil && !os.IsNotExist(err) {
		return PromiseReport{}, err
	}
	if rep.Wave == nil {
		observations = append(observations, "challenge wave not measured: the gateway logged no challenge")
	}
	rep.Observations = append(rep.Observations, observations...)
	rep.Observations = append(rep.Observations, fmt.Sprintf(
		"load-test requests tied through the mock log: %d requests, %d with their prompt found; %d executions, %d tied to one inference (%d with none, %d ambiguous)",
		link.Requests, link.Keyed, link.Executions, link.Linked, link.NoInference, link.AmbiguousExec))
	if link.LinkedPayload > 0 {
		rep.Observations = append(rep.Observations, fmt.Sprintf(
			"%d executions tied through the prompts the hosts stored, the rest by model, seed and start time", link.LinkedPayload))
	}
	if link.Replayed {
		rep.Observations = append(rep.Observations, "the run replayed requests from a dataset: they carry no tag, none is tied")
	}
	if len(in.Mock) > 0 && !complete {
		rep.Observations = append(rep.Observations, "mock log incomplete: dropped records, an unmatched execution is not absence")
	}
	return rep, nil
}

// Unknown counts the violations no known entry labels.
func (r PromiseReport) Unknown() int {
	n := 0
	for _, v := range r.Violations {
		if v.Known == "" {
			n++
		}
	}
	return n
}
