package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// logprobStore holds logprobs.content[] entries as NDJSON (one JSON value per
// line) so the fold never retains map[string]any trees (R4).
//
// Entries stay in memory while the fold RAM budget has room and spill to a
// spool file only once it does not, mirroring aggregateResponseBuffer: a
// typical logprobs answer costs no disk I/O at all. Both halves are charged to
// the request-wide foldBudget, so N choices share one RAM and one disk ceiling.
type logprobStore struct {
	mem     bytes.Buffer
	emit    []byte // spilled bytes read back at emit time; alive until close
	file    *os.File
	fileBuf *bufio.Writer
	path    string
	nMem    int64
	nDisk   int64
	entries int
	spilled bool
}

func (s *logprobStore) appendEntries(entries []json.RawMessage, emptyTop bool, b *foldBudget) error {
	for _, entry := range entries {
		raw := bytes.TrimSpace(entry)
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			continue
		}
		if emptyTop {
			rewritten, err := emptyTopLogprobsRaw(raw)
			if err != nil {
				return err
			}
			raw = rewritten
		}
		if err := s.appendRaw(raw, b); err != nil {
			return err
		}
	}
	return nil
}

func (s *logprobStore) appendRaw(raw []byte, b *foldBudget) error {
	framed, ok := frameNDJSONEntry(raw)
	if !ok {
		return nil
	}
	need := int64(len(framed) + 1)

	if !s.spilled {
		if b.ramAvailable(need) {
			if err := b.chargeRAM(need); err != nil {
				return err
			}
			s.mem.Write(framed)
			s.mem.WriteByte('\n')
			s.nMem += need
			s.entries++
			return nil
		}
		if b.spoolDir == "" {
			return fmt.Errorf("%w: logprobs ram %d+%d > %d", ErrAggregateFoldTooLarge, b.ramBytes, need, b.ramLimit)
		}
		if err := s.spill(b); err != nil {
			return err
		}
	}
	if err := b.chargeDisk(need); err != nil {
		return err
	}
	if _, err := s.fileBuf.Write(framed); err != nil {
		return err
	}
	if err := s.fileBuf.WriteByte('\n'); err != nil {
		return err
	}
	s.nDisk += need
	s.entries++
	return nil
}

// frameNDJSONEntry makes one entry safe to store as a single NDJSON line.
// json.RawMessage preserves the upstream's original byte span, so an entry that
// arrived pretty-printed (reachable through the {"events":[…]} envelope and
// bare-JSON paths, which are not line-framed) carries raw newlines that would
// otherwise split it into two invalid fragments and fail the final marshal.
func frameNDJSONEntry(raw []byte) ([]byte, bool) {
	if bytes.IndexByte(raw, '\n') < 0 {
		return raw, true
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, false
	}
	return compact.Bytes(), true
}

// spill moves the store to a spool file and reclassifies already-charged RAM
// bytes as disk bytes, so the request-wide budget stays exact across the switch.
func (s *logprobStore) spill(b *foldBudget) error {
	f, err := os.CreateTemp(b.spoolDir, "agg-lp-*.ndjson")
	if err != nil {
		return err
	}
	s.file = f
	s.path = f.Name()
	s.fileBuf = bufio.NewWriterSize(f, aggregateSpoolWriteBufferBytes)
	s.spilled = true
	if s.nMem > 0 {
		if err := b.moveToDisk(s.nMem); err != nil {
			return err
		}
		if _, err := s.fileBuf.Write(s.mem.Bytes()); err != nil {
			return err
		}
		s.nDisk += s.nMem
		s.nMem = 0
		s.mem = bytes.Buffer{} // release the buffer, not just its length
	}
	return nil
}

// contentRawMessages returns logprobs.content entries as sub-slices of the
// store's own bytes: no per-entry copy and no map[string]any rehydration. The
// results alias store memory and stay valid until close.
func (s *logprobStore) contentRawMessages() ([]json.RawMessage, error) {
	if s == nil || s.entries == 0 {
		return nil, nil
	}
	data := s.mem.Bytes()
	if s.spilled {
		if err := s.fileBuf.Flush(); err != nil {
			return nil, err
		}
		if _, err := s.file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		buf := bytes.NewBuffer(make([]byte, 0, s.nDisk))
		if _, err := buf.ReadFrom(s.file); err != nil {
			return nil, err
		}
		s.emit = buf.Bytes()
		data = s.emit
	}
	return splitNDJSON(data, s.entries), nil
}

func splitNDJSON(data []byte, hint int) []json.RawMessage {
	out := make([]json.RawMessage, 0, hint)
	for len(data) > 0 {
		var line []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line = data[:i]
			data = data[i+1:]
		} else {
			line = data
			data = nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		out = append(out, json.RawMessage(line))
	}
	return out
}

func (s *logprobStore) close() {
	if s == nil {
		return
	}
	if s.fileBuf != nil {
		_ = s.fileBuf.Flush()
		s.fileBuf = nil
	}
	if s.file != nil {
		name := s.path
		_ = s.file.Close()
		s.file = nil
		if name != "" {
			_ = os.Remove(name)
		}
		s.path = ""
	}
	s.mem = bytes.Buffer{}
	s.emit = nil
	s.nMem = 0
}

var topLogprobsKeyMarker = []byte(`"top_logprobs"`)

var emptyJSONArray = json.RawMessage(`[]`)

// emptyTopLogprobsRaw blanks top_logprobs on one content entry for clients that
// asked for logprobs but not top_logprobs. The common shape (top_logprobs only
// at the entry root) is rewritten from a shallow map of raw members, so the
// nested alternatives are never decoded; anything unusual falls back to the
// full tree walk.
func emptyTopLogprobsRaw(entry json.RawMessage) (json.RawMessage, error) {
	if !bytes.Contains(entry, topLogprobsKeyMarker) {
		return entry, nil
	}
	var shallow map[string]json.RawMessage
	if err := json.Unmarshal(entry, &shallow); err != nil {
		return entry, nil
	}
	if _, ok := shallow["top_logprobs"]; !ok {
		return deepEmptyTopLogprobsRaw(entry)
	}
	for k, v := range shallow {
		if k != "top_logprobs" && bytes.Contains(v, topLogprobsKeyMarker) {
			return deepEmptyTopLogprobsRaw(entry)
		}
	}
	shallow["top_logprobs"] = emptyJSONArray
	b, err := json.Marshal(shallow)
	if err != nil {
		return entry, err
	}
	return b, nil
}

func deepEmptyTopLogprobsRaw(entry json.RawMessage) (json.RawMessage, error) {
	var v any
	if err := json.Unmarshal(entry, &v); err != nil {
		return entry, nil
	}
	if !emptyTopLogprobs(v) {
		return entry, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return entry, err
	}
	return b, nil
}

// parseLogprobsContentRaw extracts choices[].logprobs.content as raw JSON
// values without expanding each entry into map[string]any.
func parseLogprobsContentRaw(lpRaw json.RawMessage) (content []json.RawMessage, extras map[string]json.RawMessage, err error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(lpRaw), &raw); err != nil {
		return nil, nil, err
	}
	if c, ok := raw["content"]; ok {
		trimmed := bytes.TrimSpace(c)
		if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
			if err := json.Unmarshal(trimmed, &content); err != nil {
				return nil, nil, err
			}
		}
	}
	for k, v := range raw {
		if k == "content" {
			continue
		}
		if extras == nil {
			extras = make(map[string]json.RawMessage, len(raw)-1)
		}
		extras[k] = v
	}
	return content, extras, nil
}
