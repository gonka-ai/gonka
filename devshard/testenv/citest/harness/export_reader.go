package harness

// Bounded export of a load run. Everything a result is read from is copied to a durable
// directory before any cleanup, with a content hash per file. A table or log that does not fit its
// bound is recorded as truncated with the count it could not hold: a truncated export never reads
// as a complete one. The directory must not be on tmpfs, and VerifyExport re-reads every file
// against the manifest for a caller that must prove the copy before it tears the stack down.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ExportLimits bound what one export may hold.
type ExportLimits struct {
	MaxRowsPerTable int   // rows copied from one table; 0 means 1,000,000
	MaxBytesPerFile int64 // bytes of one table dump; 0 means 512 MiB
}

func (l ExportLimits) rows() int {
	if l.MaxRowsPerTable <= 0 {
		return 1_000_000
	}
	return l.MaxRowsPerTable
}

func (l ExportLimits) bytes() int64 {
	if l.MaxBytesPerFile <= 0 {
		return 512 << 20
	}
	return l.MaxBytesPerFile
}

// ExportedFile is one file of the export.
type ExportedFile struct {
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
	Rows      int    `json:"rows,omitempty"`
	TotalRows int    `json:"total_rows,omitempty"` // rows the source held, when known
	Truncated bool   `json:"truncated,omitempty"`
	Note      string `json:"note,omitempty"`
}

// ExportManifest lists the files and says whether the export is whole.
type ExportManifest struct {
	// StartedAt is when the export began, before the first database was read; checks judge deadlines
	// against it. ExportedAt is when the export was finished.
	StartedAt  time.Time      `json:"started_at,omitempty"`
	ExportedAt time.Time      `json:"exported_at"`
	Files      []ExportedFile `json:"files"`
	Complete   bool           `json:"complete"`
	Problems   []string       `json:"problems,omitempty"`
}

const exportManifestName = "EXPORT-MANIFEST.json"

// tmpfsPrefixes are directories that live in RAM on this machine; a durable export cannot go there.
var tmpfsPrefixes = []string{"/tmp", "/run", "/dev/shm"}

// Exporter writes the export. It is safe for concurrent use.
type Exporter struct {
	dir    string
	limits ExportLimits
	mu     sync.Mutex
	files  map[string]*ExportedFile
	probs  []string
	done   bool
	start  time.Time
}

// NewExporter prepares dir. It refuses tmpfs locations and a directory that already holds an export.
func NewExporter(dir string, limits ExportLimits) (*Exporter, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for _, p := range tmpfsPrefixes {
		if abs == p || strings.HasPrefix(abs, p+"/") {
			return nil, fmt.Errorf("export directory %s is on tmpfs (%s): a durable export cannot live in RAM", abs, p)
		}
	}
	if _, err := os.Stat(filepath.Join(abs, exportManifestName)); err == nil {
		return nil, fmt.Errorf("export directory %s already holds a finished export", abs)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	return &Exporter{dir: abs, limits: limits, files: map[string]*ExportedFile{}, start: time.Now().UTC()}, nil
}

func (e *Exporter) clean(name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, "..") {
		return "", fmt.Errorf("export name %q must be a relative path without ..", name)
	}
	return filepath.Join(e.dir, name), nil
}

// WriteBytes stores one file and records its hash.
func (e *Exporter) WriteBytes(name string, data []byte) (*ExportedFile, error) {
	path, err := e.clean(name)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done {
		return nil, errors.New("export is finished")
	}
	if _, dup := e.files[name]; dup {
		return nil, fmt.Errorf("export already holds %s", name)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	f := &ExportedFile{Path: name, Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	e.files[name] = f
	return f, nil
}

// WriteJSON stores a value as indented JSON.
func (e *Exporter) WriteJSON(name string, v any) (*ExportedFile, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return e.WriteBytes(name, raw)
}

// Problem records something that makes the export incomplete.
func (e *Exporter) Problem(format string, args ...any) {
	e.mu.Lock()
	e.probs = append(e.probs, fmt.Sprintf(format, args...))
	e.mu.Unlock()
}

// Finish writes the manifest. Complete is false when any problem was recorded or any file is truncated.
func (e *Exporter) Finish() (ExportManifest, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.done = true
	m := ExportManifest{StartedAt: e.start, ExportedAt: time.Now().UTC(), Complete: true, Problems: append([]string(nil), e.probs...)}
	names := make([]string, 0, len(e.files))
	for n := range e.files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f := *e.files[n]
		m.Files = append(m.Files, f)
		if f.Truncated {
			m.Complete = false
		}
	}
	if len(m.Problems) > 0 {
		m.Complete = false
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, err
	}
	return m, os.WriteFile(filepath.Join(e.dir, exportManifestName), raw, 0o644)
}

// VerifyExport re-reads every file of a finished export and compares its hash. Run it before cleanup.
func VerifyExport(dir string) error {
	m, err := readExportManifest(dir)
	if err != nil {
		return fmt.Errorf("export manifest: %w", err)
	}
	var bad []string
	for _, f := range m.Files {
		data, err := os.ReadFile(filepath.Join(dir, f.Path))
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", f.Path, err))
			continue
		}
		sum := sha256.Sum256(data)
		if int64(len(data)) != f.Bytes || hex.EncodeToString(sum[:]) != f.SHA256 {
			bad = append(bad, fmt.Sprintf("%s: content differs from the manifest", f.Path))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("export is not intact: %s", strings.Join(bad, "; "))
	}
	return nil
}

// markTruncated flags a file already written.
func (e *Exporter) markTruncated(name string, rows, total int, note string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if f := e.files[name]; f != nil {
		f.Truncated, f.Rows, f.TotalRows, f.Note = true, rows, total, note
	}
}

func (e *Exporter) setRows(name string, rows, total int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if f := e.files[name]; f != nil {
		f.Rows, f.TotalRows = rows, total
	}
}

// ExportMock stores the mock's own counters and its full request log. A log that dropped records
// at its bound is a problem.
func ExportMock(e *Exporter, client *http.Client, mockURL, prefix string) error {
	get := func(path string) ([]byte, error) {
		resp, err := client.Get(strings.TrimRight(mockURL, "/") + path)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET %s: status %d", path, resp.StatusCode)
		}
		return io.ReadAll(resp.Body)
	}
	stats, err := get("/testenv/stats")
	if err != nil {
		e.Problem("mock stats: %v", err)
		return err
	}
	if _, err := e.WriteBytes(prefix+"mock-stats.json", stats); err != nil {
		return err
	}
	logRaw, err := get("/testenv/log")
	if err != nil {
		e.Problem("mock log: %v", err)
		return err
	}
	f, err := e.WriteBytes(prefix+"mock-log.json", logRaw)
	if err != nil {
		return err
	}
	var parsed struct {
		Complete bool `json:"complete"`
		Dropped  int  `json:"dropped"`
	}
	if err := json.Unmarshal(logRaw, &parsed); err != nil {
		e.Problem("mock log is not readable: %v", err)
		return err
	}
	if !parsed.Complete {
		e.markTruncated(f.Path, 0, 0, fmt.Sprintf("the mock dropped %d records at its bound", parsed.Dropped))
		e.Problem("mock log is incomplete: %d records dropped", parsed.Dropped)
	}
	return nil
}

// PSQL runs one statement in the run's Postgres and returns its output. An output longer than
// maxBytes is not read in: the statement stops and the call returns ErrOverBound.
type PSQL func(sql string, maxBytes int64) (string, error)

// ErrOverBound is what a PSQL returns for an output longer than its bound.
var ErrOverBound = errors.New("output over the byte bound")

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// ExportPostgres copies every ordinary and partitioned parent table (partitions come with their
// parent) as CSV with a header, in the first-column order, bounded by rows and bytes.
func ExportPostgres(e *Exporter, run PSQL, prefix string) error {
	listing, err := run(`SELECT n.nspname || '.' || c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace `+
		`WHERE c.relkind IN ('r','p') AND NOT c.relispartition AND n.nspname NOT IN ('pg_catalog','information_schema') ORDER BY 1`, e.limits.bytes())
	if err != nil {
		e.Problem("postgres table list: %v", err)
		return err
	}
	var tables []string
	for _, line := range strings.Split(strings.TrimSpace(listing), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			tables = append(tables, line)
		}
	}
	if len(tables) == 0 {
		e.Problem("postgres holds no tables to export")
		return errors.New("no tables")
	}
	var firstErr error
	for _, t := range tables {
		schema, table, _ := strings.Cut(t, ".")
		qualified := quoteIdent(schema) + "." + quoteIdent(table)
		countRaw, err := run("SELECT COUNT(*) FROM "+qualified, e.limits.bytes())
		if err != nil {
			e.Problem("postgres %s: count: %v", t, err)
			firstErr = errors.Join(firstErr, err)
			continue
		}
		total, err := strconv.Atoi(strings.TrimSpace(countRaw))
		if err != nil {
			e.Problem("postgres %s: count %q is not a number", t, countRaw)
			firstErr = errors.Join(firstErr, err)
			continue
		}
		dump, err := run(fmt.Sprintf("COPY (SELECT * FROM %s ORDER BY 1 LIMIT %d) TO STDOUT WITH CSV HEADER", qualified, e.limits.rows()), e.limits.bytes())
		if err != nil && !errors.Is(err, ErrOverBound) {
			e.Problem("postgres %s: dump: %v", t, err)
			firstErr = errors.Join(firstErr, err)
			continue
		}
		name := prefix + "postgres/" + t + ".csv"
		if err != nil || int64(len(dump)) > e.limits.bytes() {
			e.Problem("postgres %s: dump is over the %d byte bound; not written", t, e.limits.bytes())
			firstErr = errors.Join(firstErr, errors.New("dump over the byte bound"))
			continue
		}
		f, err := e.WriteBytes(name, []byte(dump))
		if err != nil {
			return err
		}
		copied := total
		if total > e.limits.rows() {
			copied = e.limits.rows()
			e.markTruncated(f.Path, copied, total, fmt.Sprintf("%d rows held, %d copied", total, copied))
			e.Problem("postgres %s: truncated at %d of %d rows", t, copied, total)
		} else {
			e.setRows(f.Path, copied, total)
		}
	}
	return firstErr
}

// FileCopier copies one file out of a service container to a local path.
type FileCopier func(service, containerPath, localPath string) error

// ExportSQLite finds the database files under dataDir in each solo service and copies them whole.
// Files are copied as they are, not queried: the SQLite files are the evidence.
func ExportSQLite(e *Exporter, services []string, dataDir string, find func(service, dir string) (string, error), cp FileCopier, prefix string) error {
	var firstErr error
	for _, svc := range services {
		listing, err := find(svc, dataDir)
		if err != nil {
			e.Problem("sqlite %s: listing %s: %v", svc, dataDir, err)
			firstErr = errors.Join(firstErr, err)
			continue
		}
		found := 0
		for _, line := range strings.Split(strings.TrimSpace(listing), "\n") {
			p := strings.TrimSpace(line)
			if p == "" {
				continue
			}
			found++
			rel := prefix + "sqlite/" + svc + "/" + strings.TrimPrefix(p, "/")
			local, err := e.clean(rel)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
				return err
			}
			if err := cp(svc, p, local); err != nil {
				e.Problem("sqlite %s: copy %s: %v", svc, p, err)
				firstErr = errors.Join(firstErr, err)
				continue
			}
			data, err := os.ReadFile(local)
			if err != nil {
				e.Problem("sqlite %s: read back %s: %v", svc, p, err)
				firstErr = errors.Join(firstErr, err)
				continue
			}
			// Hash and record the copy; the file is already in place, so it is registered as written.
			sum := sha256.Sum256(data)
			e.mu.Lock()
			e.files[rel] = &ExportedFile{Path: rel, Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
			e.mu.Unlock()
		}
		if found == 0 {
			e.Problem("sqlite %s: no database files under %s", svc, dataDir)
			firstErr = errors.Join(firstErr, errors.New("no sqlite files"))
		}
	}
	return firstErr
}

func readExportManifest(dir string) (ExportManifest, error) {
	var m ExportManifest
	raw, err := os.ReadFile(filepath.Join(dir, exportManifestName))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(raw, &m)
}

// exportGaps names what an incomplete export lost, split into what the promise checks read (the
// hosts' chains: devshard_diffs and devshard_sessions, the SQLite files and the storage modes that
// lead to them; the mock log; the chain's headers) and the rest.
func exportGaps(m ExportManifest) (judged, other []string) {
	read := func(s string) bool {
		for _, mark := range []string{"devshard_diffs", "devshard_sessions", "mock-log", "sqlite", ChainHeadersName} {
			if strings.Contains(s, mark) {
				return true
			}
		}
		for _, prefix := range []string{"postgres table list:", "host ", "mock "} {
			if strings.HasPrefix(s, prefix) && !strings.HasPrefix(s, "mock stats") {
				return true
			}
		}
		return false
	}
	gaps := append([]string(nil), m.Problems...)
	for _, f := range m.Files {
		if f.Truncated {
			gaps = append(gaps, f.Path+" truncated")
		}
	}
	for _, g := range gaps {
		if read(g) {
			judged = append(judged, g)
		} else {
			other = append(other, g)
		}
	}
	return judged, other
}
