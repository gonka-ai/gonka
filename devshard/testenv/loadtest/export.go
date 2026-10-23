package loadtest

// The export copies what the system property checks read from a running stack before it is torn
// down: every Postgres database of the run as CSV, one directory per Postgres service, the SQLite
// files of a host that keeps its sessions in them, and the request log of every mock ML node merged
// into one, each record marked with its node. It reads the stack and changes nothing in it.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockopenai"
)

// stackAccess is how the export reads a stack.
type stackAccess struct {
	psql        func(service, database string) harness.PSQL
	storageMode func(service string) (string, error)
	find        func(service, dir string) (string, error)
	copyFrom    func(service, containerPath, localPath string) error
	mockURL     func(node string) (string, error)
}

// composeStackAccess reads the stack of a compose project through the docker CLI.
func composeStackAccess(ctx context.Context, project, composePath string, cfg *config.File) stackAccess {
	compose := func(args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "docker", append([]string{"compose", "-p", project, "-f", composePath}, args...)...)
	}
	output := func(cmd *exec.Cmd) (string, error) {
		out, err := cmd.Output()
		if ee, ok := err.(*exec.ExitError); ok {
			return string(out), fmt.Errorf("%w: %s", err, ee.Stderr)
		}
		return string(out), err
	}
	return stackAccess{
		psql: func(service, database string) harness.PSQL {
			return func(sql string, maxBytes int64) (string, error) {
				return boundedOutput(compose("exec", "-T", "-e", "PGPASSWORD="+cfg.Postgres.Password, service,
					"psql", "-U", cfg.Postgres.User, "-d", database, "-q", "-At", "-v", "ON_ERROR_STOP=1", "-c", sql), maxBytes)
			}
		},
		storageMode: func(service string) (string, error) {
			out, err := output(compose("exec", "-T", service, "sh", "-c", "printf %s \"$DEVSHARD_STORAGE_MODE\""))
			return strings.TrimSpace(out), err
		},
		find: func(service, dir string) (string, error) {
			return output(compose("exec", "-T", service, "find", dir, "-type", "f",
				"(", "-name", "*.db", "-o", "-name", "*.db-wal", "-o", "-name", "*.sqlite", "-o", "-name", "*.sqlite3", ")"))
		},
		copyFrom: func(service, containerPath, localPath string) error {
			if out, err := compose("cp", service+":"+containerPath, localPath).CombinedOutput(); err != nil {
				return fmt.Errorf("docker compose cp %s: %w: %s", containerPath, err, out)
			}
			return nil
		},
		mockURL: func(node string) (string, error) {
			out, err := output(compose("port", node, fmt.Sprint(cfg.MockOpenAI.HTTPPort)))
			if err != nil {
				return "", err
			}
			addr := strings.TrimSpace(strings.Split(out, "\n")[0])
			return "http://" + strings.Replace(addr, "0.0.0.0", "127.0.0.1", 1), nil
		},
	}
}

// boundedOutput runs cmd and reads at most maxBytes of its output. A longer output stops the command
// and returns harness.ErrOverBound, so a table dump over the file bound is never held in memory.
func boundedOutput(cmd *exec.Cmd, maxBytes int64) (string, error) {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	out, err := io.ReadAll(io.LimitReader(stdout, maxBytes+1))
	if err != nil || int64(len(out)) > maxBytes {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if err == nil {
			err = harness.ErrOverBound
		}
		return "", err
	}
	if err := cmd.Wait(); err != nil {
		return string(out), fmt.Errorf("%w: %s", err, stderr.String())
	}
	return string(out), nil
}

// exportRun writes the export under outDir/export and returns its manifest. The manifest says
// whether the export is complete and lists what is missing; an error means it could not be written.
func exportRun(cfg *config.File, outDir string, stack stackAccess, client *http.Client) (harness.ExportManifest, error) {
	e, err := harness.NewExporter(filepath.Join(outDir, "export"), harness.ExportLimits{})
	if err != nil {
		return harness.ExportManifest{}, err
	}
	for _, service := range config.PostgresServices(cfg) {
		prefix := ""
		if cfg.Postgres.PerParticipant {
			prefix = "db-" + service + "/"
		}
		_ = harness.ExportPostgres(e, stack.psql(service, cfg.Postgres.Database), prefix)
	}
	// A host on SQLite keeps its chain in files in its container; a hybrid host keeps new escrows in
	// Postgres and SQLite only for the ones it served while Postgres was away, if any.
	var onSQLite []string
	for _, h := range cfg.Hosts {
		mode, err := stack.storageMode(h.ID)
		if err != nil {
			e.Problem("host %s: storage mode: %v", h.ID, err)
			continue
		}
		switch mode {
		case "sqlite":
			onSQLite = append(onSQLite, h.ID)
		case "hybrid":
			if listing, err := stack.find(h.ID, versiondDataDir); err == nil && strings.TrimSpace(listing) != "" {
				onSQLite = append(onSQLite, h.ID)
			}
		}
	}
	if len(onSQLite) > 0 {
		_ = harness.ExportSQLite(e, onSQLite, versiondDataDir, stack.find, stack.copyFrom, "")
	}

	var records []mockopenai.RequestRecord
	complete, dropped := true, 0
	nodes := cfg.MLNodeIDs()
	if len(cfg.MockOpenAI.Nodes) > 0 {
		nodes = nodes[:0]
		for _, n := range cfg.MockOpenAI.Nodes {
			nodes = append(nodes, n.Name)
		}
	}
	for _, node := range nodes {
		url, err := stack.mockURL(node)
		if err != nil {
			e.Problem("mock %s: port: %v", node, err)
			complete = false
			continue
		}
		resp, err := client.Get(url + "/testenv/log")
		if err != nil {
			e.Problem("mock %s: GET /testenv/log: %v", node, err)
			complete = false
			continue
		}
		var doc struct {
			Complete bool                       `json:"complete"`
			Dropped  int                        `json:"dropped"`
			Records  []mockopenai.RequestRecord `json:"records"`
		}
		err = json.NewDecoder(resp.Body).Decode(&doc)
		_ = resp.Body.Close()
		if err != nil {
			e.Problem("mock %s: log: %v", node, err)
			complete = false
			continue
		}
		complete = complete && doc.Complete
		dropped += doc.Dropped
		for _, r := range doc.Records {
			r.Node = node
			records = append(records, r)
		}
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].StartUnixMs < records[j].StartUnixMs })
	if _, err := e.WriteJSON("mock-log.json", map[string]any{"complete": complete, "dropped": dropped, "records": records}); err != nil {
		return harness.ExportManifest{}, err
	}
	return e.Finish()
}

// promisesHoldMargin is added to the hold so the export comes strictly after the last deadline.
const promisesHoldMargin = 30 * time.Second

// promisesHold is how long after the load the runner waits before it exports a run held to the
// promises: until an inference started or confirmed at the end of the load is due, so the deadline
// checks judge every inference of the run instead of listing it as open.
func promisesHold(refusal, execution int64) time.Duration {
	return time.Duration(max(refusal, execution))*time.Second + harness.TimeoutSkew + harness.TimeoutVoteWindow +
		harness.TimeoutSweepPeriod + harness.DeadlineSlack + promisesHoldMargin
}

// orphanWait is how long the orphan check waits for the gateway to settle: the drain, or for a run
// held to the promises the hold when it is longer. The gateway times out an inference left open only
// after its execution timeout, its grace and a sweep, which can outlast a short drain.
func (s Scenario) orphanWait() time.Duration {
	wait := s.DrainDuration()
	if !s.Assertions.Promises || s.Protocol.ExecutionTimeout == "" {
		return wait
	}
	refusal, err := protocolTimeoutSeconds(s.Protocol.RefusalTimeout)
	if err != nil {
		return wait
	}
	execution, err := protocolTimeoutSeconds(s.Protocol.ExecutionTimeout)
	if err != nil {
		return wait
	}
	return max(wait, promisesHold(refusal, execution))
}

// appendAssertionFailure adds a failed check to the run's assertion failures.
func appendAssertionFailure(existing error, check AssertionResult) error {
	if check.Passed {
		return existing
	}
	if existing == nil {
		return &assertionFailures{Checks: []AssertionResult{check}}
	}
	if failures, ok := existing.(*assertionFailures); ok {
		failures.Checks = append(failures.Checks, check)
		return failures
	}
	return existing
}

// promisesAssertion holds an exported run to the system properties and writes promises.json and
// promises.md next to it. It fails when a property is broken, the run cannot be read, or the run is
// not valid: a node with a dishonest profile that never answered dishonestly, a fault rule that never
// fired, a deadline check that judged nothing, or (when the scenario asks for it) no node ever full
// leaves the checks nothing to catch, and a node bound to a host that mock-dapi
// also handed a caller it maps to no host means the binding did not hold.
func promisesAssertion(outputDir string, scenario Scenario, profiles map[string]Profile) AssertionResult {
	check := AssertionResult{Name: "promises", Expected: "no violation of the system properties"}
	rep, err := harness.CheckLoadtestRun(outputDir, nil)
	if err != nil {
		check.Actual, check.Details = "run not read", err.Error()
		return check
	}
	mock, _, _ := harness.ReadMockLog(filepath.Join(outputDir, "export"))
	lied := map[string]int{}
	for _, r := range mock {
		if r.Phase == mockopenai.PhaseExecute && r.Chaos != "" && r.Chaos != mockopenai.ChaosOK {
			lied[r.Node]++
		}
	}
	for _, n := range scenario.Topology.MockML.Nodes {
		if p := profiles[n.Profile]; p.Chaos != nil && lied[n.Name] == 0 {
			rep.Invalid = append(rep.Invalid, fmt.Sprintf("%s (profile %s) gave no dishonest answer", n.Name, n.Profile))
		}
	}
	// A rule fires on whichever node the pool hands the request it selects, so a rule counts for the
	// run once it fired anywhere.
	// A full node queues a request for a worker or, with its queue full too, answers 503.
	fired, full := map[string]bool{}, false
	for _, r := range mock {
		if r.Rule != "" {
			fired[r.Rule] = true
		}
		full = full || r.QueuedMs > 0 || r.Outcome == "capacity_exhausted"
	}
	var ruleNames []string
	ruleNodes := map[string][]string{}
	for _, n := range scenario.Topology.MockML.Nodes {
		for _, r := range append(slices.Clone(profiles[n.Profile].Rules), scenario.Topology.MockML.Rules...) {
			if ruleNodes[r.Name] == nil {
				ruleNames = append(ruleNames, r.Name)
			}
			ruleNodes[r.Name] = append(ruleNodes[r.Name], n.Name)
		}
	}
	for _, name := range ruleNames {
		if !fired[name] {
			rep.Invalid = append(rep.Invalid, fmt.Sprintf("fault rule %s (%s) never fired", name, strings.Join(ruleNodes[name], ", ")))
		}
	}
	hosts, hostsErr := readHostStats(outputDir)
	if slices.ContainsFunc(scenario.Topology.MockML.Nodes, func(n MockMLNode) bool { return n.Host != "" }) {
		switch {
		case hostsErr != nil:
			rep.Invalid = append(rep.Invalid, fmt.Sprintf("node binding not checked: %v", hostsErr))
		case hosts[unmappedDapiHost].Allocations > 0:
			rep.Invalid = append(rep.Invalid, fmt.Sprintf("mock-dapi handed %d ML nodes to callers it maps to no host: the node binding did not hold",
				hosts[unmappedDapiHost].Allocations))
		}
	}
	if scenario.Assertions.MockML.RequireFullNode && !full {
		rep.Invalid = append(rep.Invalid, "no ML node was ever full: none queued a request for a worker or refused one at capacity")
	}
	for _, name := range []string{harness.InferenceClosesInTime, harness.ServedRequestFinished} {
		if m := rep.Checked[name]; !m.Measured {
			rep.Invalid = append(rep.Invalid, fmt.Sprintf("%s judged nothing (%s)", name, m.Note))
		}
	}
	body, err := json.MarshalIndent(rep, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(outputDir, "promises.json"), append(body, '\n'), 0o644)
	}
	if err == nil {
		var md strings.Builder
		if err = harness.WritePromiseReport(&md, "System promise checks: "+filepath.Base(filepath.Clean(outputDir)), rep); err == nil {
			err = os.WriteFile(filepath.Join(outputDir, "promises.md"), []byte(md.String()), 0o644)
		}
	}
	if err != nil {
		check.Actual, check.Details = "report not written", err.Error()
		return check
	}
	check.Passed = rep.Unknown() == 0 && len(rep.Invalid) == 0
	check.Actual = fmt.Sprintf("%d violations", len(rep.Violations))
	check.Details = "promises.md"
	if len(rep.Invalid) > 0 {
		check.Actual, check.Details = "run not valid", strings.Join(rep.Invalid, "; ")
	}
	return check
}

// readHostStats reads the mock-dapi host stats the runner wrote next to the run.
func readHostStats(outputDir string) (map[string]DapiHostStats, error) {
	raw, err := os.ReadFile(filepath.Join(outputDir, "host-stats.json"))
	if err != nil {
		return nil, err
	}
	var stats map[string]DapiHostStats
	if err := json.Unmarshal(raw, &stats); err != nil {
		return nil, fmt.Errorf("host-stats.json: %w", err)
	}
	return stats, nil
}
