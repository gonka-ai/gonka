package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"

	"devshard/internal/jsonshape"
	"devshard/spool"
)

// logprobStore holds logprobs.content[] entries as NDJSON (one JSON value per
// line) so the fold never retains map[string]any trees (R4).
//
// Entries stay in memory while the fold RAM budget has room and spill through
// spool.Dir (slot + unlink-at-create) once it does not. Both halves are charged
// to the request-wide foldBudget.
type logprobStore struct {
	mem     bytes.Buffer
	file    *spool.File
	nMem    int64
	nDisk   int64
	entries int
	spilled bool
}

func (s *logprobStore) appendEntries(entries iter.Seq[[]byte], emptyTop bool, strip []string, b *foldBudget) error {
	for raw := range entries {
		if bytes.Equal(raw, []byte("null")) {
			continue
		}
		if s.entries >= aggregateMaxLogprobEntries {
			return fmt.Errorf("%w: logprobs entries > %d", ErrAggregateFoldTooLarge, aggregateMaxLogprobEntries)
		}
		raw, err := rewriteLogprobEntry(raw, emptyTop, strip)
		if err != nil {
			return err
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
	line := append(append([]byte(nil), framed...), '\n')
	if _, err := s.file.Write(line); err != nil {
		if errors.Is(err, spool.ErrFileTooLarge) {
			return fmt.Errorf("%w: logprobs disk", ErrAggregateFoldTooLarge)
		}
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

// spill moves the store onto a spool.Dir file (slot + anonymous inode) and
// reclassifies already-charged RAM bytes as disk bytes.
func (s *logprobStore) spill(b *foldBudget) error {
	dir := currentAggregateDir()
	if dir == nil || !dir.Enabled() {
		return fmt.Errorf("%w: logprobs spool unavailable", ErrAggregateFoldTooLarge)
	}
	f, err := dir.Create()
	if err != nil {
		if errors.Is(err, spool.ErrNoCapacity) {
			return fmt.Errorf("%w: logprobs spool capacity", ErrAggregateFoldTooLarge)
		}
		return fmt.Errorf("%w: logprobs spill: %v", ErrAggregateFoldTooLarge, err)
	}
	s.file = f
	s.spilled = true
	if s.nMem > 0 {
		if err := b.moveToDisk(s.nMem); err != nil {
			_ = f.Close()
			s.file = nil
			s.spilled = false
			return err
		}
		if _, err := f.Write(s.mem.Bytes()); err != nil {
			_ = f.Close()
			s.file = nil
			s.spilled = false
			return err
		}
		s.nDisk += s.nMem
		s.nMem = 0
		s.mem = bytes.Buffer{}
	}
	return nil
}

func (s *logprobStore) contentRawArray() (json.RawMessage, error) {
	if s == nil || s.entries == 0 {
		return nil, nil
	}
	encoded := make(json.RawMessage, s.nMem+s.nDisk+1)
	if s.spilled {
		r, err := s.file.Reader()
		if err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(r, encoded[1:]); err != nil {
			return nil, err
		}
	} else {
		copy(encoded[1:], s.mem.Bytes())
	}
	encoded[0], encoded[len(encoded)-1] = '[', ']'
	for i := 1; i < len(encoded)-1; i++ {
		if encoded[i] == '\n' {
			encoded[i] = ','
		}
	}
	return encoded, nil
}

func (s *logprobStore) close() {
	if s == nil {
		return
	}
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
	s.mem = bytes.Buffer{}
	s.nMem = 0
}

// corruptForTest closes the underlying spool fd so emit-time reads fail.
func (s *logprobStore) corruptForTest() error {
	if s == nil || s.file == nil {
		return errors.New("no spilled file")
	}
	return s.file.CorruptForTest()
}

var topLogprobsKeyMarker = []byte(`"top_logprobs"`)

func rewriteLogprobEntry(entry []byte, emptyTop bool, strip []string) ([]byte, error) {
	found := bytes.Contains(entry, []byte(`\u`)) || (emptyTop && bytes.Contains(entry, topLogprobsKeyMarker))
	if !found {
		for _, name := range strip {
			if bytes.Contains(entry, []byte(`"`+name+`"`)) {
				found = true
				break
			}
		}
	}
	if !found {
		return entry, nil
	}
	out, _, err := jsonshape.Rewrite(entry, func(key []byte, _ int) (string, bool) {
		for _, name := range strip {
			if jsonshape.KeyEqual(key, name) {
				return "", true
			}
		}
		if emptyTop && jsonshape.KeyEqual(key, "top_logprobs") {
			return "[]", true
		}
		return "", false
	})
	if err != nil {
		aggregateRejectedShapeTotal.Add(1)
		return nil, fmt.Errorf("%w: logprobs entry: %w", ErrAggregateFoldTooLarge, err)
	}
	return out, nil
}

func parseLogprobsContentRaw(lpRaw json.RawMessage) (iter.Seq[[]byte], map[string]json.RawMessage, error) {
	if _, err := jsonshape.Check(lpRaw, jsonshape.Limits{RootMembers: aggregateMaxObjectMembers}); err != nil {
		return nil, nil, err
	}
	content := jsonshape.Field(lpRaw, "content")
	if len(content) > 0 && content[0] != '[' && !bytes.Equal(content, []byte("null")) {
		return func(func([]byte) bool) {}, nil, nil
	}
	var extras map[string]json.RawMessage
	jsonshape.Object(lpRaw, func(key, raw []byte) bool {
		if !jsonshape.KeyEqual(key, "content") {
			if extras == nil {
				extras = make(map[string]json.RawMessage)
			}
			name := string(key)
			if bytes.ContainsRune(key, '\\') {
				quoted := append(append([]byte{'"'}, key...), '"')
				_ = json.Unmarshal(quoted, &name)
			}
			extras[name] = raw
		}
		return true
	})
	return func(yield func([]byte) bool) { jsonshape.Array(content, yield) }, extras, nil
}
