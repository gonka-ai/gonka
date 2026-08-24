package host

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// spoolIndexEntryBytes is the fixed width of one .idx record: the absolute
// offset in the .log file at which a content event starts.
const spoolIndexEntryBytes = 8

// ErrSpoolClosed is returned when a spool read races the release of a finished
// generation. Callers treat it like any other resume failure.
var ErrSpoolClosed = errors.New("live stream spool closed")

// streamSpool is the per-generation scratch log that backs live-stream resume.
//
// It is deliberately *not* durable state: no fsync, removed when the generation
// releases it, and swept wholesale at startup. Losing it costs at most one
// reconnect, exactly like losing the in-RAM window did before it existed. The
// durable artifact stays the payload store entry written at finish.
//
// Two files per generation:
//
//	<escrow>-<inference>.log  raw producer bytes, byte-addressed
//	<escrow>-<inference>.idx  int64 little-endian, entry k = absolute .log
//	                          offset where content event k starts
//
// The index is what lets a reconnect resolve a wire cursor
// (delivered_events, delivered_partial) that is older than the RAM window, so
// the resume horizon is disk retention rather than hot RAM.
type streamSpool struct {
	mu     sync.Mutex
	log    *os.File
	idx    *os.File
	bytes  int64 // .log bytes written and readable
	events int64 // content events indexed
	closed bool

	logPath string
	idxPath string
}

// PrepareLiveStreamSpoolDir empties and recreates dir. Any file left there is
// from a previous process: the spool is scratch, so a crash leftover is garbage
// rather than recoverable state.
func PrepareLiveStreamSpoolDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("spool: empty dir")
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("spool: clear %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("spool: create %s: %w", dir, err)
	}
	return nil
}

// spoolFileStem builds a single path segment for one generation. escrowID comes
// from applied escrow state, but it is still untrusted enough to keep out of a
// path: anything that is not a plain segment is rejected.
func spoolFileStem(escrowID string, inferenceID uint64) (string, error) {
	escrowID = strings.TrimSpace(escrowID)
	if escrowID == "" {
		return "", fmt.Errorf("spool: empty escrowID")
	}
	if strings.ContainsAny(escrowID, `/\`) || strings.Contains(escrowID, "..") ||
		filepath.Base(escrowID) != escrowID {
		return "", fmt.Errorf("spool: invalid escrowID")
	}
	return fmt.Sprintf("%s-%d", escrowID, inferenceID), nil
}

func newStreamSpool(dir, escrowID string, inferenceID uint64) (*streamSpool, error) {
	stem, err := spoolFileStem(escrowID, inferenceID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("spool: create %s: %w", dir, err)
	}
	sp := &streamSpool{
		logPath: filepath.Join(dir, stem+".log"),
		idxPath: filepath.Join(dir, stem+".idx"),
	}
	// O_TRUNC: a same-name leftover belongs to a dead generation.
	flags := os.O_CREATE | os.O_RDWR | os.O_TRUNC
	if sp.log, err = os.OpenFile(sp.logPath, flags, 0o600); err != nil {
		return nil, fmt.Errorf("spool: open log: %w", err)
	}
	if sp.idx, err = os.OpenFile(sp.idxPath, flags, 0o600); err != nil {
		_ = sp.log.Close()
		_ = os.Remove(sp.logPath)
		return nil, fmt.Errorf("spool: open index: %w", err)
	}
	return sp, nil
}

// append writes payload bytes and the index entries for the content events that
// start inside them. Called only from the spool pump goroutine, never from the
// producer's Write path.
//
// The log is written before the index so a torn append can only under-report
// events, never point the index at bytes that are not there.
func (sp *streamSpool) append(payload []byte, eventStarts []int64) error {
	if len(payload) == 0 {
		return nil
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.closed {
		return ErrSpoolClosed
	}
	if _, err := sp.log.Write(payload); err != nil {
		return fmt.Errorf("spool: append log: %w", err)
	}
	if len(eventStarts) > 0 {
		buf := make([]byte, len(eventStarts)*spoolIndexEntryBytes)
		for i, off := range eventStarts {
			binary.LittleEndian.PutUint64(buf[i*spoolIndexEntryBytes:], uint64(off))
		}
		if _, err := sp.idx.Write(buf); err != nil {
			return fmt.Errorf("spool: append index: %w", err)
		}
		sp.events += int64(len(eventStarts))
	}
	sp.bytes += int64(len(payload))
	return nil
}

// readAt copies up to len(dst) bytes starting at absolute offset off. The caller
// must not ask for bytes beyond what append has accepted.
func (sp *streamSpool) readAt(dst []byte, off int64) (int, error) {
	sp.mu.Lock()
	closed, size := sp.closed, sp.bytes
	sp.mu.Unlock()
	if closed {
		return 0, ErrSpoolClosed
	}
	if off >= size {
		return 0, nil
	}
	if int64(len(dst)) > size-off {
		dst = dst[:size-off]
	}
	n, err := sp.log.ReadAt(dst, off)
	if err != nil && n > 0 {
		// Short read against a file that is still growing is not an error for
		// the caller: it advances by n and comes back.
		return n, nil
	}
	if err != nil {
		return n, fmt.Errorf("spool: read log at %d: %w", off, err)
	}
	return n, nil
}

// eventOffset returns the absolute .log offset where content event i starts.
func (sp *streamSpool) eventOffset(i int64) (int64, error) {
	sp.mu.Lock()
	closed, events := sp.closed, sp.events
	sp.mu.Unlock()
	if closed {
		return 0, ErrSpoolClosed
	}
	if i < 0 || i >= events {
		return 0, ErrResumeCursorPast
	}
	var buf [spoolIndexEntryBytes]byte
	if _, err := sp.idx.ReadAt(buf[:], i*spoolIndexEntryBytes); err != nil {
		return 0, fmt.Errorf("spool: read index %d: %w", i, err)
	}
	return int64(binary.LittleEndian.Uint64(buf[:])), nil
}

// close releases the file handles and removes both files. Safe to call twice.
func (sp *streamSpool) close() {
	sp.mu.Lock()
	if sp.closed {
		sp.mu.Unlock()
		return
	}
	sp.closed = true
	log, idx := sp.log, sp.idx
	sp.log, sp.idx = nil, nil
	sp.mu.Unlock()

	if log != nil {
		_ = log.Close()
	}
	if idx != nil {
		_ = idx.Close()
	}
	_ = os.Remove(sp.logPath)
	_ = os.Remove(sp.idxPath)
}
