package loadtest

// The export copies what the system property checks read from a running stack before it is torn
// down: every Postgres database of the run as CSV, one directory per Postgres service, the SQLite
// files of a host that keeps its sessions in them, the request log of every mock ML node merged
// into one, each record marked with its node, and the block headers mock-chain still serves, whose
// times the timeouts run on. It reads the stack and changes nothing in it.

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
	"sort"
	"strings"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockopenai"
)

// versiondDataDir is where a host keeps its SQLite files inside its container.
const versiondDataDir = "/opt/versiond/data"

// stackAccess is how the export reads a stack.
type stackAccess struct {
	psql        func(service, database string) harness.PSQL
	storageMode func(service string) (string, error)
	find        func(service, dir string) (string, error)
	copyFrom    func(service, containerPath, localPath string) error
	mockURL     func(node string) (string, error)
	chainURL    func() (string, error) // mock-chain's CometBFT RPC
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
		chainURL: func() (string, error) {
			out, err := output(compose("port", "mock-chain", fmt.Sprint(cfg.MockChain.RPCPort)))
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
	var headers []harness.ChainHeader
	url, err := stack.chainURL()
	if err == nil {
		headers, err = readChainHeaders(client, url)
	}
	if err != nil {
		e.Problem("%s: %v", harness.ChainHeadersName, err)
	}
	if _, err := e.WriteJSON(harness.ChainHeadersName, map[string]any{"headers": headers}); err != nil {
		return harness.ExportManifest{}, err
	}
	return e.Finish()
}

// readChainHeaders reads every block header mock-chain serves, from the tip down to the lowest height
// it keeps, through CometBFT's blockchain route (at most 20 headers a call).
func readChainHeaders(client *http.Client, rpcURL string) ([]harness.ChainHeader, error) {
	var out []harness.ChainHeader
	for maxHeight := int64(0); ; {
		url := fmt.Sprintf("%s/blockchain?minHeight=1&maxHeight=%d", rpcURL, maxHeight)
		resp, err := client.Get(url)
		if err != nil {
			return out, err
		}
		var doc struct {
			Result struct {
				BlockMetas []struct {
					BlockID struct {
						Hash string `json:"hash"`
					} `json:"block_id"`
					Header struct {
						Height int64     `json:"height,string"`
						Time   time.Time `json:"time"`
					} `json:"header"`
				} `json:"block_metas"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
				Data    string `json:"data"`
			} `json:"error"`
		}
		err = json.NewDecoder(resp.Body).Decode(&doc)
		_ = resp.Body.Close()
		switch {
		case err != nil:
			return out, fmt.Errorf("GET %s: %w", url, err)
		case doc.Error != nil && len(out) > 0:
			return out, nil // below the lowest height kept
		case doc.Error != nil:
			return out, fmt.Errorf("GET %s: %s %s", url, doc.Error.Message, doc.Error.Data)
		}
		lowest := int64(0)
		for _, m := range doc.Result.BlockMetas {
			out = append(out, harness.ChainHeader{Height: uint64(m.Header.Height), Hash: strings.ToLower(m.BlockID.Hash), TimeMs: m.Header.Time.UnixMilli()})
			if lowest == 0 || m.Header.Height < lowest {
				lowest = m.Header.Height
			}
		}
		if len(doc.Result.BlockMetas) < 20 || lowest <= 1 {
			return out, nil
		}
		maxHeight = lowest - 1
	}
}

// promisesAssertion holds an exported run to the system properties and writes report.json and
// report.md next to it. It fails when a property is broken, the run cannot be read, or the run is
// not valid: a node with a dishonest profile that never answered dishonestly leaves nothing for the
// checks to catch.
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
	body, err := json.MarshalIndent(rep, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(outputDir, "report.json"), append(body, '\n'), 0o644)
	}
	if err == nil {
		var md strings.Builder
		if err = harness.WritePromiseReport(&md, "System promise checks: "+filepath.Base(filepath.Clean(outputDir)), rep); err == nil {
			err = os.WriteFile(filepath.Join(outputDir, "report.md"), []byte(md.String()), 0o644)
		}
	}
	if err != nil {
		check.Actual, check.Details = "report not written", err.Error()
		return check
	}
	check.Passed = rep.Unknown() == 0 && len(rep.Invalid) == 0
	check.Actual = fmt.Sprintf("%d violations", len(rep.Violations))
	check.Details = "report.md"
	if len(rep.Invalid) > 0 {
		check.Actual, check.Details = "run not valid", strings.Join(rep.Invalid, "; ")
	}
	return check
}
