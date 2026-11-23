// Package streamevidence holds which lines of an error stream are evidence, how many of them a gateway retains, and how they serialize into the payload a verifier rehashes. See README.md.
package streamevidence

import (
	"bytes"
	"strings"

	"common/completionapi"
)

// MaxRetainedBytes bounds the evidence a gateway retains of one attempt, each kept line counted as its bytes plus one separator.
const MaxRetainedBytes = 64 << 10

// DataLines returns the evidence lines of a read of SSE events: its data lines with a trailing carriage return trimmed, without the devshard receipt and meta envelopes.
func DataLines(events []byte) []string {
	if len(events) == 0 {
		return nil
	}
	var lines []string
	for raw := range bytes.SplitSeq(events, []byte(lineSeparator)) {
		line := strings.TrimRight(string(raw), carriageReturn)
		if !strings.HasPrefix(line, completionapi.DataPrefix) || isEnvelopeLine(line) {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// IsDoneLine reports whether line is the data line that ends a stream.
func IsDoneLine(line string) bool {
	return strings.TrimSpace(strings.TrimPrefix(line, completionapi.DataPrefix)) == doneMarker
}

func isEnvelopeLine(line string) bool {
	data := strings.TrimPrefix(line, completionapi.DataPrefix)
	return strings.Contains(data, quotedReceiptKey) || strings.Contains(data, quotedMetaKey)
}
