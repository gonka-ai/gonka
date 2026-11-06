package filters

import "bytes"

// eachSSELine visits every "data:" line's trimmed payload, terminator included, stopping when visit says so.
func eachSSELine(events []byte, visit func(payload []byte) bool) {
	for rest := events; len(rest) > 0; {
		var line []byte
		line, rest, _ = bytes.Cut(rest, sseLineSeparator)
		data, isData := bytes.CutPrefix(bytes.TrimRight(line, "\r"), sseDataParsePrefix)
		if !isData {
			continue
		}
		if visit(bytes.TrimSpace(data)) {
			return
		}
	}
}

// EachSSEDataPayload visits every "data:" payload, skipping empty lines and [DONE].
func EachSSEDataPayload(events []byte, visit func(payload []byte) bool) {
	eachSSELine(events, func(payload []byte) bool {
		if len(payload) == 0 || bytes.Equal(payload, sseDoneMarker) {
			return false
		}
		return visit(payload)
	})
}

// SSEEventTerminated reports whether these bytes end an event, so a caller does not glue the next one on.
func SSEEventTerminated(events []byte) bool {
	return bytes.HasSuffix(events, sseEventSeparator) || bytes.HasSuffix(events, sseEventSeparatorCRLF)
}

// TrimSSEDone drops a terminating [DONE] so the gateway writes its own. See ../api/README.md, "Streaming the reply".
func TrimSSEDone(events []byte) []byte {
	if !HasSSEDone(events) {
		return events
	}
	cut := bytes.LastIndex(events, sseDataParsePrefix)
	if cut < 0 {
		return events
	}
	return bytes.TrimRight(events[:cut], jsonWhitespace)
}

// HasSSEDone is line-anchored, so a "[DONE]" inside a content delta is not read as the terminator.
func HasSSEDone(events []byte) bool {
	terminated := false
	eachSSELine(events, func(payload []byte) bool {
		terminated = bytes.Equal(payload, sseDoneMarker)
		return terminated
	})
	return terminated
}

// eventPayload joins the event's data lines as a client does, handing back a single-line event as a slice of it rather than a copy.
func eventPayload(event []byte) (dataLines int, payload []byte, held bool) {
	var joined []byte
	for offset := 0; offset < len(event); {
		line, lineEnd := event[offset:], len(event)
		if breakAt := bytes.IndexByte(line, '\n'); breakAt >= 0 {
			line, lineEnd = line[:breakAt], offset+breakAt+1
		}
		offset = lineEnd
		data, isData := bytes.CutPrefix(bytes.TrimRight(line, "\r"), sseDataParsePrefix)
		if !isData {
			continue
		}
		data = bytes.TrimLeft(data, " \t")
		dataLines++
		if dataLines == 1 {
			payload = data
			continue
		}
		if joined == nil {
			joined = append(make([]byte, 0, len(payload)+len(data)+1), payload...)
		}
		if len(joined) > 0 {
			joined = append(joined, '\n')
		}
		joined = append(joined, data...)
		payload = joined
	}
	return dataLines, payload, dataLines > 0
}

// rebuildEvent replaces the data lines and keeps every other one: a client reads event, id and retry from them.
func rebuildEvent(event, payload []byte) []byte {
	rewritten := make([]byte, 0, len(event)+len(payload))
	emitted := false
	for offset := 0; offset < len(event); {
		line, lineEnd := event[offset:], len(event)
		if breakAt := bytes.IndexByte(line, '\n'); breakAt >= 0 {
			line, lineEnd = line[:breakAt], offset+breakAt+1
		}
		if _, isData := bytes.CutPrefix(bytes.TrimRight(line, "\r"), sseDataParsePrefix); !isData {
			rewritten = append(rewritten, event[offset:lineEnd]...)
			offset = lineEnd
			continue
		}
		if !emitted {
			segments := 0
			for segment := range bytes.SplitSeq(payload, sseLineSeparator) {
				if segments > 0 {
					rewritten = append(rewritten, '\n')
				}
				segments++
				rewritten = append(rewritten, sseDataPrefix...)
				rewritten = append(rewritten, segment...)
			}
			rewritten = append(rewritten, event[offset+len(line):lineEnd]...)
			emitted = true
		}
		offset = lineEnd
	}
	return rewritten
}

// forEachSSEEvent visits each complete event and then whatever trailing bytes carried no terminator.
func forEachSSEEvent(stream []byte, visit func(event []byte) bool) {
	for rest := stream; len(rest) > 0; {
		offset := indexEventEnd(rest)
		if offset < 0 {
			visit(rest)
			return
		}
		if visit(rest[:offset]) {
			return
		}
		rest = rest[offset:]
	}
}

// indexEventEnd returns the offset past the first LF or CRLF terminator, or -1; it walks line by line to stay linear.
func indexEventEnd(buf []byte) int {
	for offset := 0; offset < len(buf); {
		lineEnd := bytes.IndexByte(buf[offset:], '\n')
		if lineEnd < 0 {
			return -1
		}
		next := offset + lineEnd + 1
		switch {
		case next < len(buf) && buf[next] == '\n':
			return next + 1
		case next+1 < len(buf) && buf[next] == '\r' && buf[next+1] == '\n':
			return next + crlfLength
		}
		offset = next
	}
	return -1
}
