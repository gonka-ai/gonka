package loadtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

// fakeStack answers the export's reads of a stack from memory.
type fakeStack struct {
	tables  map[string]map[string]string // service -> table -> CSV
	modes   map[string]string            // service -> DEVSHARD_STORAGE_MODE
	sqlite  map[string][]string          // service -> files under the data dir
	mocks   map[string]string            // node -> base URL
	copied  []string
	queried []string
}

func (f *fakeStack) access() stackAccess {
	return stackAccess{
		psql: func(service, database string) harness.PSQL {
			return func(sql string, _ int64) (string, error) {
				f.queried = append(f.queried, service+"/"+database)
				tables := f.tables[service]
				switch {
				case strings.HasPrefix(sql, "SELECT n.nspname"):
					var names []string
					for name := range tables {
						names = append(names, name)
					}
					return strings.Join(names, "\n"), nil
				case strings.HasPrefix(sql, "SELECT COUNT(*)"):
					for name, csv := range tables {
						if strings.Contains(sql, `"`+strings.Split(name, ".")[1]+`"`) {
							return fmt.Sprint(strings.Count(strings.TrimSpace(csv), "\n")), nil
						}
					}
				case strings.HasPrefix(sql, "COPY"):
					for name, csv := range tables {
						if strings.Contains(sql, `"`+strings.Split(name, ".")[1]+`"`) {
							return csv, nil
						}
					}
				}
				return "", fmt.Errorf("unexpected statement %q", sql)
			}
		},
		storageMode: func(service string) (string, error) { return f.modes[service], nil },
		find: func(service, dir string) (string, error) {
			return strings.Join(f.sqlite[service], "\n"), nil
		},
		copyFrom: func(service, containerPath, localPath string) error {
			f.copied = append(f.copied, service+":"+containerPath)
			return os.WriteFile(localPath, []byte("sqlite of "+service), 0o644)
		},
		mockURL: func(node string) (string, error) {
			url, ok := f.mocks[node]
			if !ok {
				return "", fmt.Errorf("no port for %s", node)
			}
			return url, nil
		},
	}
}

// durableDir is a fresh directory next to the test: the export refuses tmpfs, where t.TempDir()
// usually lives.
func durableDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", "export-test-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func mockWithRequests(t *testing.T, n int) string {
	t.Helper()
	ts := httptest.NewServer(mockopenai.NewServer(mockopenai.DefaultConfig()).Handler())
	t.Cleanup(ts.Close)
	for i := 0; i < n; i++ {
		resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
			strings.NewReader(fmt.Sprintf(`{"model":"test-model","seed":%d,"messages":[{"role":"user","content":"x"}]}`, i)))
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	}
	return ts.URL
}

func exportConfig() *config.File {
	cfg := &config.File{}
	cfg.Postgres.Enabled = true
	cfg.Postgres.PerParticipant = true
	cfg.Postgres.Database = "devshard"
	cfg.Hosts = []config.HostCfg{{ID: "versiond-0"}, {ID: "versiond-2"}}
	cfg.MockOpenAI.Nodes = []config.MockOpenAINodeCfg{{Name: "mock-openai-0"}, {Name: "mock-openai-1"}}
	return cfg
}

// Test flow:
//  1. Two mock nodes have answered requests; two Postgres services hold a table each; one host keeps
//     its sessions in SQLite, the other in hybrid storage with no SQLite file.
//  2. Export the run.
//  3. Check the merged request log names the node of every record, every database is copied under
//     its service, the SQLite host's file is copied, and the export is complete.
func TestExportRunCopiesTheDatabasesAndTheMergedRequestLog(t *testing.T) {
	cfg := exportConfig()
	services := config.PostgresServices(cfg)
	require.Len(t, services, 2)
	stack := &fakeStack{
		tables: map[string]map[string]string{
			services[0]: {"public.devshard_diffs": "escrow_id,nonce\n1,1\n1,2\n"},
			services[1]: {"public.devshard_diffs": "escrow_id,nonce\n1,1\n"},
		},
		modes:  map[string]string{"versiond-0": "sqlite", "versiond-2": "hybrid"},
		sqlite: map[string][]string{"versiond-0": {"/opt/versiond/data/escrow-1.db"}},
		mocks:  map[string]string{"mock-openai-0": mockWithRequests(t, 2), "mock-openai-1": mockWithRequests(t, 3)},
	}
	out := durableDir(t)
	m, err := exportRun(cfg, out, stack.access(), http.DefaultClient)
	require.NoError(t, err)
	require.True(t, m.Complete, "%v", m.Problems)
	require.NoError(t, harness.VerifyExport(filepath.Join(out, "export")))

	for _, service := range services {
		_, err := os.Stat(filepath.Join(out, "export", "db-"+service, "postgres", "public.devshard_diffs.csv"))
		require.NoError(t, err, service)
	}
	require.Equal(t, []string{"versiond-0:/opt/versiond/data/escrow-1.db"}, stack.copied, "only the host on SQLite is copied")

	raw, err := os.ReadFile(filepath.Join(out, "export", "mock-log.json"))
	require.NoError(t, err)
	var log struct {
		Complete bool                       `json:"complete"`
		Records  []mockopenai.RequestRecord `json:"records"`
	}
	require.NoError(t, json.Unmarshal(raw, &log))
	require.True(t, log.Complete)
	require.Len(t, log.Records, 5)
	byNode := map[string]int{}
	for _, rec := range log.Records {
		byNode[rec.Node]++
	}
	require.Equal(t, map[string]int{"mock-openai-0": 2, "mock-openai-1": 3}, byNode)
}

// Test flow:
// 1. One mock node cannot be reached.
// 2. Export the run.
// 3. Check the merged request log and the export both say they are incomplete.
func TestExportRunMarksAnUnreachableMockIncomplete(t *testing.T) {
	cfg := exportConfig()
	services := config.PostgresServices(cfg)
	stack := &fakeStack{
		tables: map[string]map[string]string{
			services[0]: {"public.devshard_diffs": "escrow_id,nonce\n1,1\n"},
			services[1]: {"public.devshard_diffs": "escrow_id,nonce\n1,1\n"},
		},
		modes: map[string]string{"versiond-0": "postgres", "versiond-2": "hybrid"},
		mocks: map[string]string{"mock-openai-0": mockWithRequests(t, 1)},
	}
	out := durableDir(t)
	m, err := exportRun(cfg, out, stack.access(), http.DefaultClient)
	require.NoError(t, err)
	require.False(t, m.Complete)
	require.NotEmpty(t, m.Problems)

	raw, err := os.ReadFile(filepath.Join(out, "export", "mock-log.json"))
	require.NoError(t, err)
	var log struct {
		Complete bool `json:"complete"`
	}
	require.NoError(t, json.Unmarshal(raw, &log))
	require.False(t, log.Complete, "a node that could not be read leaves the log incomplete")
}

// Test flow:
//  1. Run a command that writes without end, one whose output fits the bound, and one that fails.
//  2. Read each through boundedOutput with a 100-byte bound.
//  3. Check the endless one is stopped at the bound with ErrOverBound instead of being read in, the
//     short one comes back whole, and the failure carries the command's stderr.
func TestBoundedOutputStopsACommandAtTheBound(t *testing.T) {
	_, err := boundedOutput(exec.Command("yes"), 100)
	require.ErrorIs(t, err, harness.ErrOverBound)

	out, err := boundedOutput(exec.Command("printf", "%0100d", "0"), 100)
	require.NoError(t, err)
	require.Len(t, out, 100)

	_, err = boundedOutput(exec.Command("sh", "-c", "echo boom >&2; exit 3"), 100)
	require.ErrorContains(t, err, "boom")
}
