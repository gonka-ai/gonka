package loadtest

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	chain   string                       // mock-chain RPC base URL, empty when it cannot be reached
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
		chainURL: func() (string, error) {
			if f.chain == "" {
				return "", fmt.Errorf("no port for mock-chain")
			}
			return f.chain, nil
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
//     its service, the SQLite host's file is copied, every header the chain keeps (heights 6 to 50,
//     three pages) is exported, and the export is complete.
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
		chain:  newUnixChain(t, 6, 50).URL,
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

	headers, err := harness.ReadChainHeaders(filepath.Join(out, "export"))
	require.NoError(t, err)
	require.Len(t, headers, 45)
	for h := uint64(6); h <= 50; h++ {
		require.Equal(t, int64(h)*1000, headers[h].TimeMs, "height %d", h)
		require.Equal(t, hex.EncodeToString(unixBlockHash(h)), headers[h].Hash, "height %d", h)
	}
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

// unixChain is a CometBFT RPC serving heights lowest to tip, the block at height h made at unix
// second h; it counts the header reads.
type unixChain struct {
	*httptest.Server
	headerReads atomic.Int32
}

func unixBlockHash(height uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], height)
	sum := sha256.Sum256(b[:])
	return sum[:]
}

func newUnixChain(t *testing.T, lowest, tip int64) *unixChain {
	c := &unixChain{}
	type header struct {
		Height string    `json:"height"`
		Time   time.Time `json:"time"`
	}
	headerAt := func(h int64) header { return header{Height: strconv.FormatInt(h, 10), Time: time.Unix(h, 0).UTC()} }
	reply := func(w http.ResponseWriter, result any, err string) {
		body := map[string]any{"jsonrpc": "2.0", "id": -1}
		if err != "" {
			w.WriteHeader(http.StatusInternalServerError)
			body["error"] = map[string]any{"code": -32603, "message": "Internal error", "data": err}
		} else {
			body["result"] = result
		}
		_ = json.NewEncoder(w).Encode(body)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/header", func(w http.ResponseWriter, r *http.Request) {
		c.headerReads.Add(1)
		h, _ := strconv.ParseInt(r.URL.Query().Get("height"), 10, 64)
		if h < lowest || h > tip {
			reply(w, nil, "height "+strconv.FormatInt(h, 10)+" is not available")
			return
		}
		reply(w, map[string]any{"header": headerAt(h)}, "")
	})
	mux.HandleFunc("/blockchain", func(w http.ResponseWriter, r *http.Request) {
		minHeight, _ := strconv.ParseInt(r.URL.Query().Get("minHeight"), 10, 64)
		maxHeight, _ := strconv.ParseInt(r.URL.Query().Get("maxHeight"), 10, 64)
		if maxHeight == 0 {
			maxHeight = tip
		}
		maxHeight = min(maxHeight, tip)
		minHeight = max(minHeight, lowest, maxHeight-19)
		if minHeight > maxHeight {
			reply(w, nil, "min height "+strconv.FormatInt(minHeight, 10)+" can't be greater than max height "+strconv.FormatInt(maxHeight, 10))
			return
		}
		var metas []map[string]any
		for h := maxHeight; h >= minHeight; h-- {
			metas = append(metas, map[string]any{
				"block_id": map[string]any{"hash": strings.ToUpper(hex.EncodeToString(unixBlockHash(uint64(h))))},
				"header":   headerAt(h),
			})
		}
		reply(w, map[string]any{"last_height": strconv.FormatInt(tip, 10), "block_metas": metas}, "")
	})
	c.Server = httptest.NewServer(mux)
	t.Cleanup(c.Close)
	return c
}
