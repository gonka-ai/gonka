// Package logcapture collects log lines a test asserts on, in one place rather than in every package
// that has a line worth asserting.
package logcapture

import (
	"reflect"
	"sync"
	"testing"

	"devshard/logging"
)

// Entry is one recorded line: its level, its message, and the key-value pairs it carried.
type Entry struct {
	Level  string
	Msg    string
	Fields []any
}

// Recorder is a logging.Logger that keeps what it was given.
type Recorder struct {
	mu      sync.Mutex
	entries []Entry
}

func (r *Recorder) record(level, msg string, fields []any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, Entry{Level: level, Msg: msg, Fields: fields})
}

func (r *Recorder) Info(msg string, fields ...any)  { r.record("info", msg, fields) }
func (r *Recorder) Warn(msg string, fields ...any)  { r.record("warn", msg, fields) }
func (r *Recorder) Error(msg string, fields ...any) { r.record("error", msg, fields) }
func (r *Recorder) Debug(msg string, fields ...any) { r.record("debug", msg, fields) }

// All returns a copy of everything recorded so far.
func (r *Recorder) All() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Entry(nil), r.entries...)
}

// Find returns the first entry with this message.
func (r *Recorder) Find(msg string) (Entry, bool) {
	for _, entry := range r.All() {
		if entry.Msg == msg {
			return entry, true
		}
	}
	return Entry{}, false
}

var (
	installedMu sync.Mutex
	installed   *Recorder
)

// Install makes the recorder the gateway's logger for the length of the test. The logger is a package
// global, so a test that installs one cannot run in parallel with another that reads it.
func Install(t *testing.T) *Recorder {
	t.Helper()
	recorder := &Recorder{}
	logging.SetLogger(recorder)
	installedMu.Lock()
	installed = recorder
	installedMu.Unlock()
	t.Cleanup(func() {
		installedMu.Lock()
		installed = nil
		installedMu.Unlock()
		logging.SetLogger(logging.NewSlogAdapter())
	})
	return recorder
}

// Shared returns the recorder the test already installed, or installs one, so two readers of one test's log never replace each other's.
func Shared(t *testing.T) *Recorder {
	t.Helper()
	installedMu.Lock()
	current := installed
	installedMu.Unlock()
	if current != nil {
		return current
	}
	return Install(t)
}

// Field returns the value logged under key, or nil when the entry did not carry it.
func Field(entry Entry, key string) any {
	for i := 0; i+1 < len(entry.Fields); i += 2 {
		if name, held := entry.Fields[i].(string); held && name == key {
			return entry.Fields[i+1]
		}
	}
	return nil
}

// Contains reports whether a line with exactly this level, message and typed fields was recorded.
func (r *Recorder) Contains(want Entry) bool {
	for _, entry := range r.All() {
		if reflect.DeepEqual(entry, want) {
			return true
		}
	}
	return false
}

// RequireLine fails the test unless a line identical to want was recorded.
func (r *Recorder) RequireLine(t *testing.T, want Entry) {
	t.Helper()
	if !r.Contains(want) {
		t.Fatalf("no line %+v among %+v", want, r.All())
	}
}
