package filters

import (
	"bytes"
	"fmt"
)

type BodyFolder struct {
	intent       LogprobIntent
	carry        []byte
	scanned      int
	unframed     []byte
	merged       map[string]any
	template     *contentTemplate
	complete     []byte
	framed       bool
	assembled    int
	truncated    bool
	failed       error
	mergedBytes  int64
	sinceMeasure int64
}

// Re-measure interval, in merged bytes rather than events. See README.md, "Folding a stream into one body".
const foldMeasureBytes = 256 << 10

func NewBodyFolder(intent LogprobIntent) *BodyFolder {
	return &BodyFolder{intent: intent}
}

func (f *BodyFolder) Write(chunk []byte) (int, error) {
	if f.failed != nil {
		return 0, f.failed
	}
	f.carry = append(f.carry, chunk...)

	eventStart, searchFrom := 0, f.scanned
	for {
		offset := indexEventEnd(f.carry[searchFrom:])
		if offset < 0 {
			break
		}
		eventEnd := searchFrom + offset
		f.fold(f.carry[eventStart:eventEnd])
		eventStart, searchFrom = eventEnd, eventEnd
	}
	f.carry = append(f.carry[:0], f.carry[eventStart:]...)
	f.scanned = max(0, len(f.carry)-len(sseEventSeparatorCRLF)+1)

	if len(f.carry) > MaxStreamCarryBytes {
		carried := len(f.carry)
		f.carry, f.scanned, f.unframed = nil, 0, nil
		f.failed = fmt.Errorf("%w: %d bytes", ErrStreamCarryOverflow, carried)
		return 0, f.failed
	}
	return len(chunk), nil
}

func (f *BodyFolder) fold(event []byte) {
	if f.assembled >= maxAssembledEvents {
		f.truncated = true
		return
	}
	_, payload, held := eventPayload(event)
	if !held {
		if !f.framed {
			f.unframed = append(f.unframed, event...)
		}
		return
	}
	f.framed, f.unframed = true, nil
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || bytes.Equal(payload, sseDoneMarker) {
		return
	}
	if f.template != nil && f.template.appendChunk(payload) {
		f.countAssembled(payload)
		return
	}
	decoded, parsed := decodeStreamedEvent(payload)
	if !parsed {
		return
	}
	if name, isString := decoded["object"].(string); isString && name == completionObject {
		if f.merged == nil && f.complete == nil {
			f.complete = append([]byte(nil), payload...)
		}
		return
	}
	if f.complete != nil {
		return
	}
	deleteFields(decoded, f.intent.strippedFields())
	if f.intent.Keep && !f.intent.KeepTop {
		emptyTopLogprobs(decoded)
	}
	f.merged = mergeChunk(f.merged, decoded)
	f.template = newContentTemplate(payload, decoded, f.merged, f.intent)
	f.countAssembled(payload)
}

func (f *BodyFolder) countAssembled(payload []byte) {
	f.assembled++
	f.sinceMeasure += int64(len(payload))
	if f.sinceMeasure >= foldMeasureBytes {
		f.measure()
	}
}

// measure re-encodes without finalising: finalising rewrites the deltas the fold is still appending to.
func (f *BodyFolder) measure() {
	f.sinceMeasure = 0
	if f.merged == nil {
		f.mergedBytes = 0
		return
	}
	encoded, err := encodeCompact(f.merged)
	if err != nil {
		return
	}
	f.mergedBytes = int64(len(encoded))
}

func (f *BodyFolder) Body() []byte {
	if f.failed != nil {
		return NoResponseDataBody
	}
	if len(f.carry) > 0 {
		f.fold(f.carry)
		f.carry, f.scanned = nil, 0
	}
	switch {
	case f.truncated:
		return TruncatedResponseBody
	case f.complete != nil:
		return stripResponseBody(f.complete, f.intent)
	case f.merged != nil:
		return encodeCompletion(f.merged)
	case f.framed || len(bytes.TrimSpace(f.unframed)) == 0:
		return NoResponseDataBody
	}
	return stripResponseBody(f.unframed, f.intent)
}

func (f *BodyFolder) Held() int64 {
	return f.mergedBytes + int64(len(f.carry)+len(f.unframed)+len(f.complete))
}

func (f *BodyFolder) Discard() {
	f.carry, f.unframed, f.merged, f.complete, f.template = nil, nil, nil, nil, nil
	f.scanned, f.mergedBytes, f.sinceMeasure = 0, 0, 0
}
