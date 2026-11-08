package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

// exportDir returns a fresh directory that is not under a tmpfs prefix: next to the package, removed after.
func exportDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", "export-test-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func newExporter(t *testing.T, limits ExportLimits) (*Exporter, string) {
	t.Helper()
	dir := exportDir(t)
	e, err := NewExporter(dir, limits)
	require.NoError(t, err)
	abs, err := filepath.Abs(dir)
	require.NoError(t, err)
	return e, abs
}

func TestExporterRefusesTmpfsAndReusedDirectories(t *testing.T) {
	for _, bad := range []string{"/tmp/x", "/tmp", "/dev/shm/run", "/run/user/1000/x"} {
		_, err := NewExporter(bad, ExportLimits{})
		require.Error(t, err, bad)
		require.Contains(t, err.Error(), "tmpfs", bad)
	}
	// A name that only begins like a tmpfs path is not one.
	e, dir := newExporter(t, ExportLimits{})
	_, err := e.Finish()
	require.NoError(t, err)
	_, err = NewExporter(dir, ExportLimits{})
	require.Error(t, err, "a directory that holds a finished export is not reused")
	require.Contains(t, err.Error(), "already holds a finished export")
}

func TestExporterHashesVerifiesAndDetectsTamper(t *testing.T) {
	e, dir := newExporter(t, ExportLimits{})
	f, err := e.WriteBytes("a/one.txt", []byte("hello"))
	require.NoError(t, err)
	require.Equal(t, "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", f.SHA256, "sha256 of hello")
	require.EqualValues(t, 5, f.Bytes)
	_, err = e.WriteJSON("two.json", map[string]int{"k": 1})
	require.NoError(t, err)

	m, err := e.Finish()
	require.NoError(t, err)
	require.True(t, m.Complete)
	require.Empty(t, m.Problems)
	require.Equal(t, []string{"a/one.txt", "two.json"}, []string{m.Files[0].Path, m.Files[1].Path}, "files are listed in name order")
	require.NoError(t, VerifyExport(dir))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a/one.txt"), []byte("hellO"), 0o644))
	err = VerifyExport(dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "a/one.txt")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a/one.txt"), []byte("hello"), 0o644))
	require.NoError(t, VerifyExport(dir))

	require.NoError(t, os.Remove(filepath.Join(dir, "two.json")))
	require.Error(t, VerifyExport(dir), "a missing file is not intact")
	require.Error(t, VerifyExport(filepath.Join(dir, "nowhere")))
}

func TestExporterRefusesBadNamesDuplicatesAndLateWrites(t *testing.T) {
	e, _ := newExporter(t, ExportLimits{})
	for _, bad := range []string{"", "../x", "/abs", "a/../../x"} {
		_, err := e.WriteBytes(bad, []byte("x"))
		require.Error(t, err, bad)
	}
	_, err := e.WriteBytes("x", []byte("1"))
	require.NoError(t, err)
	_, err = e.WriteBytes("x", []byte("2"))
	require.Error(t, err, "a file is not silently overwritten")
	_, err = e.Finish()
	require.NoError(t, err)
	_, err = e.WriteBytes("y", []byte("3"))
	require.Error(t, err, "nothing is written after Finish")
}

func TestAProblemMakesTheExportIncomplete(t *testing.T) {
	e, dir := newExporter(t, ExportLimits{})
	_, err := e.WriteBytes("x", []byte("1"))
	require.NoError(t, err)
	e.Problem("the table %s could not be read", "t")
	m, err := e.Finish()
	require.NoError(t, err)
	require.False(t, m.Complete)
	require.Equal(t, []string{"the table t could not be read"}, m.Problems)
	raw, err := os.ReadFile(filepath.Join(dir, exportManifestName))
	require.NoError(t, err)
	var onDisk ExportManifest
	require.NoError(t, json.Unmarshal(raw, &onDisk))
	require.False(t, onDisk.Complete, "the manifest on disk says so as well")
}

func TestMockExportCarriesCountersAndTheWholeLog(t *testing.T) {
	ts := httptest.NewServer(mockopenai.NewServer(mockopenai.DefaultConfig()).Handler())
	t.Cleanup(ts.Close)
	for i := 0; i < 3; i++ {
		resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
			strings.NewReader(fmt.Sprintf(`{"model":"test-model","seed":%d,"messages":[{"role":"user","content":"x"}]}`, i)))
		require.NoError(t, err)
		resp.Body.Close()
	}

	e, dir := newExporter(t, ExportLimits{})
	require.NoError(t, ExportMock(e, http.DefaultClient, ts.URL+"/", "run1/"))
	m, err := e.Finish()
	require.NoError(t, err)
	require.True(t, m.Complete, "%v", m.Problems)
	require.NoError(t, VerifyExport(dir))

	raw, err := os.ReadFile(filepath.Join(dir, "run1/mock-log.json"))
	require.NoError(t, err)
	var log struct {
		Complete bool                       `json:"complete"`
		Records  []mockopenai.RequestRecord `json:"records"`
	}
	require.NoError(t, json.Unmarshal(raw, &log))
	require.True(t, log.Complete)
	require.Len(t, log.Records, 3)
	stats, err := os.ReadFile(filepath.Join(dir, "run1/mock-stats.json"))
	require.NoError(t, err)
	var counters mockopenai.Stats
	require.NoError(t, json.Unmarshal(stats, &counters))
	require.Equal(t, 3, counters.Requests[mockopenai.PhaseExecute])
	require.Equal(t, 3, counters.LogRecords)
}

func TestAMockLogThatDroppedRecordsIsFlaggedNotAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/testenv/stats" {
			_, _ = w.Write([]byte(`{"active":0}`))
			return
		}
		_, _ = w.Write([]byte(`{"complete":false,"dropped":7,"records":[]}`))
	}))
	t.Cleanup(srv.Close)
	e, _ := newExporter(t, ExportLimits{})
	require.NoError(t, ExportMock(e, http.DefaultClient, srv.URL, ""))
	m, err := e.Finish()
	require.NoError(t, err)
	require.False(t, m.Complete)
	require.Contains(t, strings.Join(m.Problems, ";"), "7 records dropped")
	var logFile ExportedFile
	for _, f := range m.Files {
		if f.Path == "mock-log.json" {
			logFile = f
		}
	}
	require.True(t, logFile.Truncated)
}

func TestATruncatedFileAloneMakesTheExportIncomplete(t *testing.T) {
	e, _ := newExporter(t, ExportLimits{})
	f, err := e.WriteBytes("t.csv", []byte("id\n1\n"))
	require.NoError(t, err)
	e.markTruncated(f.Path, 1, 9, "1 of 9")
	m, err := e.Finish()
	require.NoError(t, err)
	require.Empty(t, m.Problems, "no separate problem is recorded here")
	require.False(t, m.Complete, "the truncation flag alone decides")
}

func TestMockExportReportsAnErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/testenv/log" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	e, _ := newExporter(t, ExportLimits{})
	err := ExportMock(e, http.DefaultClient, srv.URL, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "status 500")
	m, ferr := e.Finish()
	require.NoError(t, ferr)
	require.False(t, m.Complete)
	for _, f := range m.Files {
		require.NotEqual(t, "mock-log.json", f.Path, "an error body is not stored as the log")
	}
}

func TestMockExportReportsAnUnreachableMock(t *testing.T) {
	e, _ := newExporter(t, ExportLimits{})
	require.Error(t, ExportMock(e, http.DefaultClient, "http://127.0.0.1:1", ""))
	m, err := e.Finish()
	require.NoError(t, err)
	require.False(t, m.Complete)
	require.NotEmpty(t, m.Problems)
}

// fakePSQL answers by statement; every statement it saw is kept.
type fakePSQL struct {
	tables map[string]string // "schema.table" -> csv dump
	counts map[string]int
	fail   map[string]bool // table -> dump fails
	seen   []string
}

func (f *fakePSQL) run(sql string, maxBytes int64) (string, error) {
	f.seen = append(f.seen, sql)
	switch {
	case strings.Contains(sql, "FROM pg_class"):
		var names []string
		for n := range f.tables {
			names = append(names, n)
		}
		sortStrings(names)
		return strings.Join(names, "\n") + "\n", nil
	case strings.HasPrefix(sql, "SELECT COUNT(*) FROM "):
		return fmt.Sprintf("%d\n", f.counts[unquote(strings.TrimPrefix(sql, "SELECT COUNT(*) FROM "))]), nil
	case strings.HasPrefix(sql, "COPY (SELECT * FROM "):
		rest := strings.TrimPrefix(sql, "COPY (SELECT * FROM ")
		name := unquote(rest[:strings.Index(rest, " ORDER BY")])
		if f.fail[name] {
			return "", errors.New("permission denied")
		}
		if int64(len(f.tables[name])) > maxBytes {
			return "", ErrOverBound
		}
		return f.tables[name], nil
	}
	return "", fmt.Errorf("unexpected statement %q", sql)
}

func sortStrings(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

// unquote turns "public"."t" into public.t.
func unquote(q string) string {
	parts := strings.Split(q, `"."`)
	for i := range parts {
		parts[i] = strings.Trim(parts[i], `"`)
		parts[i] = strings.ReplaceAll(parts[i], `""`, `"`)
	}
	return strings.Join(parts, ".")
}

func TestPostgresExportCopiesEveryParentTableWithItsCounts(t *testing.T) {
	f := &fakePSQL{
		tables: map[string]string{
			"public.devshard_diffs":             "id,payload\n1,aaa\n2,bbb\n",
			"public.devshard_validation_leases": "inference_id,status\n7,pending\n",
		},
		counts: map[string]int{"public.devshard_diffs": 2, "public.devshard_validation_leases": 1},
	}
	e, dir := newExporter(t, ExportLimits{MaxRowsPerTable: 10})
	require.NoError(t, ExportPostgres(e, f.run, "run1/"))
	m, err := e.Finish()
	require.NoError(t, err)
	require.True(t, m.Complete, "%v", m.Problems)
	require.NoError(t, VerifyExport(dir))

	require.Len(t, m.Files, 2)
	require.Equal(t, "run1/postgres/public.devshard_diffs.csv", m.Files[0].Path)
	require.Equal(t, 2, m.Files[0].Rows)
	require.Equal(t, 2, m.Files[0].TotalRows)
	data, err := os.ReadFile(filepath.Join(dir, m.Files[0].Path))
	require.NoError(t, err)
	require.Equal(t, "id,payload\n1,aaa\n2,bbb\n", string(data), "the dump is stored as received, payload column included")

	all := strings.Join(f.seen, "\n")
	require.Contains(t, all, "NOT c.relispartition", "partitions come with their parent, not as separate dumps")
	require.Contains(t, all, `"public"."devshard_diffs" ORDER BY 1 LIMIT 10) TO STDOUT WITH CSV HEADER`, "bounded and in a fixed order")
}

func TestPostgresExportFlagsATableLargerThanItsBound(t *testing.T) {
	f := &fakePSQL{
		tables: map[string]string{"public.big": "id\n1\n2\n3\n"},
		counts: map[string]int{"public.big": 50},
	}
	e, _ := newExporter(t, ExportLimits{MaxRowsPerTable: 3})
	require.NoError(t, ExportPostgres(e, f.run, ""))
	m, err := e.Finish()
	require.NoError(t, err)
	require.False(t, m.Complete)
	require.True(t, m.Files[0].Truncated)
	require.Equal(t, 3, m.Files[0].Rows)
	require.Equal(t, 50, m.Files[0].TotalRows, "the count the source held is kept")
	require.Contains(t, strings.Join(m.Problems, ";"), "truncated at 3 of 50 rows")
}

func TestPostgresExportKeepsGoingPastOneBadTableAndSaysSo(t *testing.T) {
	f := &fakePSQL{
		tables: map[string]string{"public.good": "id\n1\n", "public.denied": "id\n9\n"},
		counts: map[string]int{"public.good": 1, "public.denied": 1},
		fail:   map[string]bool{"public.denied": true},
	}
	e, _ := newExporter(t, ExportLimits{})
	err := ExportPostgres(e, f.run, "")
	require.Error(t, err)
	m, ferr := e.Finish()
	require.NoError(t, ferr)
	require.False(t, m.Complete)
	require.Len(t, m.Files, 1, "the readable table is still exported")
	require.Equal(t, "postgres/public.good.csv", m.Files[0].Path)
	require.Contains(t, strings.Join(m.Problems, ";"), "public.denied")
}

func TestPostgresExportRefusesToWriteADumpOverTheByteBound(t *testing.T) {
	f := &fakePSQL{
		tables: map[string]string{"public.fat": strings.Repeat("x", 100)},
		counts: map[string]int{"public.fat": 1},
	}
	e, _ := newExporter(t, ExportLimits{MaxBytesPerFile: 50})
	require.Error(t, ExportPostgres(e, f.run, ""))
	m, err := e.Finish()
	require.NoError(t, err)
	require.False(t, m.Complete)
	require.Empty(t, m.Files, "a dump that does not fit is not written half")
	require.Contains(t, strings.Join(m.Problems, ";"), "over the 50 byte bound")
}

func TestPostgresExportQuotesOddIdentifiersAndRefusesAnEmptyDatabase(t *testing.T) {
	f := &fakePSQL{
		tables: map[string]string{`public.we"ird`: "id\n1\n"},
		counts: map[string]int{`public.we"ird`: 1},
	}
	e, _ := newExporter(t, ExportLimits{})
	require.NoError(t, ExportPostgres(e, f.run, ""))
	require.Contains(t, strings.Join(f.seen, "\n"), `"public"."we""ird"`)

	empty := &fakePSQL{tables: map[string]string{}}
	e2, _ := newExporter(t, ExportLimits{})
	require.Error(t, ExportPostgres(e2, empty.run, ""), "a database with no tables is a problem, not an empty success")
	m, err := e2.Finish()
	require.NoError(t, err)
	require.False(t, m.Complete)
}

func TestSQLiteExportCopiesFilesAndRecordsTheirHashes(t *testing.T) {
	e, dir := newExporter(t, ExportLimits{})
	find := func(service, d string) (string, error) {
		require.Equal(t, "/opt/versiond/data", d)
		if service == "versiond-2" {
			return "/opt/versiond/data/a.db\n/opt/versiond/data/sub/b.sqlite\n", nil
		}
		return "", nil
	}
	cp := func(service, from, to string) error {
		return os.WriteFile(to, []byte("db:"+service+":"+from), 0o644)
	}
	err := ExportSQLite(e, []string{"versiond-2", "versiond-3"}, "/opt/versiond/data", find, cp, "run1/")
	require.Error(t, err, "versiond-3 has no files")
	m, ferr := e.Finish()
	require.NoError(t, ferr)
	require.False(t, m.Complete)
	require.Contains(t, strings.Join(m.Problems, ";"), "versiond-3: no database files")
	require.Len(t, m.Files, 2)
	require.Equal(t, "run1/sqlite/versiond-2/opt/versiond/data/a.db", m.Files[0].Path)
	require.NoError(t, VerifyExport(dir))
	data, err := os.ReadFile(filepath.Join(dir, m.Files[0].Path))
	require.NoError(t, err)
	require.Equal(t, "db:versiond-2:/opt/versiond/data/a.db", string(data))
}

func TestSQLiteExportReportsACopyThatFails(t *testing.T) {
	e, _ := newExporter(t, ExportLimits{})
	err := ExportSQLite(e, []string{"s"}, "/d",
		func(string, string) (string, error) { return "/d/x.db\n", nil },
		func(string, string, string) error { return errors.New("no such container") }, "")
	require.Error(t, err)
	m, ferr := e.Finish()
	require.NoError(t, ferr)
	require.False(t, m.Complete)
	require.Empty(t, m.Files)
	require.Contains(t, strings.Join(m.Problems, ";"), "no such container")
}
