package gatewayproto

import (
	"bytes"
	"encoding/json"
	"strings"

	"common/completionapi"
)

// EvidenceCollector retains the host's original SSE data lines within a byte
// budget. One attempt owns it; reserve/refund callbacks must not reenter it.
type EvidenceCollector struct {
	maxBytes  int
	reserve   func(int) bool
	refund    func(int)
	lines     []string
	bytesHeld int
	complete  bool
	truncated bool
	released  bool
}

func NewEvidenceCollector(maxBytes int, reserve func(int) bool, refund func(int)) *EvidenceCollector {
	return &EvidenceCollector{maxBytes: maxBytes, reserve: reserve, refund: refund}
}

func (r *EvidenceCollector) AppendEvents(events []byte) {
	if r == nil || r.released || r.truncated || len(events) == 0 {
		return
	}
	r.AppendLines(EventDataLines(events))
}

func (r *EvidenceCollector) AppendLines(lines []string) {
	if r == nil || r.released || r.truncated {
		return
	}
	for _, line := range lines {
		held := len(line) + 1
		if r.bytesHeld+held > r.maxBytes {
			r.truncated = true
			return
		}
		if r.reserve != nil && !r.reserve(held) {
			r.truncated = true
			return
		}
		r.lines = append(r.lines, line)
		r.bytesHeld += held
		if strings.TrimSpace(strings.TrimPrefix(line, completionapi.DataPrefix)) == "[DONE]" {
			r.complete = true
		}
	}
}

func (r *EvidenceCollector) MarkTruncated() {
	if r != nil {
		r.truncated = true
	}
}
func (r *EvidenceCollector) Complete() bool  { return r != nil && r.complete }
func (r *EvidenceCollector) Truncated() bool { return r != nil && r.truncated }
func (r *EvidenceCollector) BytesHeld() int {
	if r == nil {
		return 0
	}
	return r.bytesHeld
}
func (r *EvidenceCollector) Lines() []string {
	if r == nil {
		return nil
	}
	return r.lines
}

func (r *EvidenceCollector) Payload() []byte {
	if r == nil || len(r.lines) == 0 {
		return nil
	}
	payload, err := json.Marshal(completionapi.SerializedStreamedResponse{Events: r.lines})
	if err != nil {
		return nil
	}
	return payload
}

func (r *EvidenceCollector) clear() {
	if r.refund != nil && r.bytesHeld > 0 {
		r.refund(r.bytesHeld)
	}
	r.lines, r.bytesHeld = nil, 0
}

// Reset drops retained bytes but keeps this collector available for the next
// retention window. A released collector is never reopened.
func (r *EvidenceCollector) Reset() {
	if r == nil || r.released {
		return
	}
	r.clear()
	r.complete, r.truncated = false, false
}

// Release permanently stops retention and returns any held budget once.
func (r *EvidenceCollector) Release() {
	if r == nil || r.released {
		return
	}
	r.clear()
	r.released = true
}

// EventDataLines excludes protocol envelopes without rewriting host events.
func EventDataLines(events []byte) []string {
	var lines []string
	for raw := range bytes.SplitSeq(events, []byte("\n")) {
		line := strings.TrimRight(string(raw), "\r")
		if !strings.HasPrefix(line, completionapi.DataPrefix) || IsProtocolDataLine(line) {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func IsProtocolDataLine(line string) bool {
	data := strings.TrimPrefix(line, completionapi.DataPrefix)
	return strings.Contains(data, `"devshard_receipt"`) || strings.Contains(data, `"devshard_meta"`)
}
