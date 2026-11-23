package jsonshape

import (
	"bytes"
	"errors"
	"fmt"
)

const MaxDepth = 128

var (
	ErrInvalid         = errors.New("invalid json")
	ErrLimit           = errors.New("json shape exceeds limit")
	ErrTooDeep         = fmt.Errorf("%w: nesting deeper than %d", ErrLimit, MaxDepth)
	ErrRootMembers     = fmt.Errorf("%w: root members", ErrLimit)
	ErrRootKeyElements = fmt.Errorf("%w: root key elements", ErrLimit)
	ErrObjectMembers   = fmt.Errorf("%w: object members", ErrLimit)
	ErrArrayElements   = fmt.Errorf("%w: array elements", ErrLimit)
	ErrNodes           = fmt.Errorf("%w: nodes", ErrLimit)
)

type Limits struct {
	RootMembers     int
	RootKeyElements map[string]int
	ObjectMembers   int
	ArrayElements   int
	Nodes           int
}

type Stats struct {
	RootMembers      int
	MaxObjectMembers int
	MaxArrayElements int
	Nodes            int
	Depth            int
}

type frame struct {
	members   int
	limit     int
	nonEmpty  bool
	object    bool
	expectKey bool
}

const (
	classPlain uint8 = iota
	classSkip
	classOpenObject
	classOpenArray
	classClose
	classComma
	classQuote
)

var byteClass = func() [256]uint8 {
	var t [256]uint8
	for _, c := range []byte{' ', '\t', '\n', '\r', ':'} {
		t[c] = classSkip
	}
	t['{'] = classOpenObject
	t['['] = classOpenArray
	t['}'] = classClose
	t[']'] = classClose
	t[','] = classComma
	t['"'] = classQuote
	return t
}()

func Check(raw []byte, l Limits) (Stats, error) {
	var s scanner
	s.limits = l
	err := s.run(raw)
	return s.st, err
}

func Measure(raw []byte) (Stats, error) {
	return Check(raw, Limits{})
}

type scanner struct {
	raw        []byte
	limits     Limits
	st         Stats
	stack      [MaxDepth]frame
	depth      int
	keyStart   int
	keyEnd     int
	keyEscaped bool
}

func (s *scanner) run(raw []byte) error {
	s.raw = raw
	for i := 0; i < len(raw); i++ {
		switch byteClass[raw[i]] {
		case classSkip:
		case classPlain:
			if err := s.value(); err != nil {
				return err
			}
			for i+1 < len(raw) && byteClass[raw[i+1]] == classPlain {
				i++
			}
		case classOpenObject, classOpenArray:
			if err := s.open(byteClass[raw[i]] == classOpenObject); err != nil {
				return err
			}
		case classClose:
			if s.depth == 0 {
				return nil
			}
			s.depth--
		case classComma:
			if err := s.comma(); err != nil {
				return err
			}
		case classQuote:
			end := skipString(raw, i+1)
			if end < 0 {
				return nil
			}
			if s.depth > 0 && s.stack[s.depth-1].expectKey {
				f := &s.stack[s.depth-1]
				f.expectKey = false
				if err := s.mark(f); err != nil {
					return err
				}
				if s.depth == 1 && s.limits.RootKeyElements != nil {
					s.keyStart, s.keyEnd = i+1, end
					s.keyEscaped = bytes.IndexByte(raw[i+1:end], '\\') >= 0
				}
			} else if err := s.value(); err != nil {
				return err
			}
			i = end
		}
	}
	return nil
}

func (s *scanner) value() error {
	if s.depth == 0 {
		if s.st.Nodes == 0 {
			s.st.Nodes = 1
		}
		return nil
	}
	return s.mark(&s.stack[s.depth-1])
}

func (s *scanner) open(object bool) error {
	limit := 0
	if !object && s.depth == 1 && s.stack[0].object && s.limits.RootKeyElements != nil {
		limit = s.rootKeyLimit()
	}
	if err := s.value(); err != nil {
		return err
	}
	if s.depth == MaxDepth {
		return ErrTooDeep
	}
	s.stack[s.depth] = frame{object: object, expectKey: object, limit: limit}
	s.depth++
	if s.depth > s.st.Depth {
		s.st.Depth = s.depth
	}
	return nil
}

func (s *scanner) rootKeyLimit() int {
	key := s.raw[s.keyStart:s.keyEnd]
	if !s.keyEscaped {
		return s.limits.RootKeyElements[string(key)]
	}
	for name, limit := range s.limits.RootKeyElements {
		if limit > 0 && KeyEqual(key, name) {
			return limit
		}
	}
	return 0
}

func (s *scanner) comma() error {
	if s.depth == 0 {
		return nil
	}
	f := &s.stack[s.depth-1]
	f.members++
	f.expectKey = f.object
	if err := s.limitMembers(f); err != nil {
		return err
	}
	s.st.Nodes++
	if s.limits.Nodes > 0 && s.st.Nodes > s.limits.Nodes {
		return ErrNodes
	}
	return nil
}

func (s *scanner) mark(f *frame) error {
	if f.nonEmpty {
		return nil
	}
	f.nonEmpty = true
	f.members = 1
	if err := s.limitMembers(f); err != nil {
		return err
	}
	s.st.Nodes++
	if s.limits.Nodes > 0 && s.st.Nodes > s.limits.Nodes {
		return ErrNodes
	}
	return nil
}

func (s *scanner) limitMembers(f *frame) error {
	n := f.members
	l := &s.limits
	root := s.depth == 1
	if root && n > s.st.RootMembers {
		s.st.RootMembers = n
	}
	if f.object {
		if n > s.st.MaxObjectMembers {
			s.st.MaxObjectMembers = n
		}
	} else if n > s.st.MaxArrayElements {
		s.st.MaxArrayElements = n
	}
	switch {
	case root && l.RootMembers > 0 && n > l.RootMembers:
		return ErrRootMembers
	case f.limit > 0 && n > f.limit:
		return ErrRootKeyElements
	case f.object && l.ObjectMembers > 0 && n > l.ObjectMembers:
		return ErrObjectMembers
	case !f.object && l.ArrayElements > 0 && n > l.ArrayElements:
		return ErrArrayElements
	}
	return nil
}

func skipString(raw []byte, i int) int {
	for i < len(raw) {
		j := bytes.IndexByte(raw[i:], '"')
		if j < 0 {
			return -1
		}
		end := i + j
		backslashes := 0
		for k := end - 1; k >= i && raw[k] == '\\'; k-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return end
		}
		i = end + 1
	}
	return -1
}
