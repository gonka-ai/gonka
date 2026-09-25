package completionapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"hash"
	"strings"
)

const doneLine = DataPrefix + "[DONE]"

type envelopeHasher struct {
	digest    hash.Hash
	lineCount int
	sum       *[32]byte
}

type ReceivedResponseHasher struct {
	envelope      *envelopeHasher
	bodyLineCount int
	firstBody     string
	isBareBody    bool
	isComplete    bool
}

type servedEnvelope struct {
	Events []json.RawMessage `json:"events"`
}

func newEnvelopeHasher() *envelopeHasher {
	hasher := &envelopeHasher{digest: sha256.New()}
	hasher.digest.Write([]byte(`{"events":[`))
	return hasher
}

func (hasher *envelopeHasher) add(line string) {
	if hasher.lineCount > 0 {
		hasher.digest.Write([]byte(","))
	}
	encoded, _ := json.Marshal(line)
	hasher.digest.Write(encoded)
	hasher.lineCount++
}

func (hasher *envelopeHasher) finish() [32]byte {
	if hasher.sum == nil {
		hasher.digest.Write([]byte("]}"))
		var sum [32]byte
		copy(sum[:], hasher.digest.Sum(nil))
		hasher.sum = &sum
	}
	return *hasher.sum
}

func NewReceivedResponseHasher() *ReceivedResponseHasher {
	return &ReceivedResponseHasher{envelope: newEnvelopeHasher()}
}

func (hasher *ReceivedResponseHasher) Add(line string) {
	if line == "" {
		return
	}
	if hasher.envelope.lineCount == 0 {
		hasher.isBareBody = strings.HasPrefix(line, DataPrefix) && line != doneLine
		hasher.firstBody = strings.TrimPrefix(line, DataPrefix)
	}
	if line == doneLine {
		hasher.isComplete = true
	} else {
		hasher.bodyLineCount++
	}
	hasher.envelope.add(line)
}

func (hasher *ReceivedResponseHasher) MarkComplete() {
	hasher.isComplete = true
}

func (hasher *ReceivedResponseHasher) Sums() [][32]byte {
	if !hasher.isComplete || hasher.envelope.lineCount == 0 {
		return nil
	}
	sums := [][32]byte{hasher.envelope.finish()}
	if hasher.isBareBody && hasher.bodyLineCount == 1 {
		sums = append(sums, sha256.Sum256([]byte(hasher.firstBody)))
	}
	return sums
}

func StripForGateway(stored []byte) ([]byte, error) {
	if events, isEnvelope := envelopeEvents(stored); isEnvelope {
		served := make([]json.RawMessage, 0, len(events))
		for _, event := range events {
			var line string
			if err := json.Unmarshal(event, &line); err != nil {
				return nil, err
			}
			stripped := stripStreamedLine(line)
			if stripped == line {
				served = append(served, event)
				continue
			}
			encoded, err := json.Marshal(stripped)
			if err != nil {
				return nil, err
			}
			served = append(served, encoded)
		}
		return json.Marshal(servedEnvelope{Events: served})
	}
	document, err := decodeJSONDocument(stored)
	if err != nil {
		return nil, err
	}
	if _, isObject := document.(map[string]any); !isObject {
		return nil, errors.New("strip for gateway: stored payload is not a JSON object")
	}
	dropFields(document, fieldsOnlyAskingCallersSee)
	return json.Marshal(document)
}

func envelopeEvents(stored []byte) ([]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(stored, &fields); err != nil || len(fields) != 1 {
		return nil, false
	}
	var events []json.RawMessage
	if err := json.Unmarshal(fields["events"], &events); err != nil || events == nil {
		return nil, false
	}
	for _, event := range events {
		if !bytes.HasPrefix(bytes.TrimSpace(event), []byte(`"`)) {
			return nil, false
		}
	}
	return events, true
}

func stripStreamedLine(line string) string {
	body, isData := streamedLineBody(line)
	if !isData {
		return line
	}
	document, err := decodeJSONDocument([]byte(body))
	if err != nil {
		return line
	}
	if _, isObject := document.(map[string]any); !isObject {
		return line
	}
	if !dropFields(document, fieldsOnlyAskingCallersSee) {
		return line
	}
	stripped, err := json.Marshal(document)
	if err != nil {
		return line
	}
	return DataPrefix + string(stripped)
}
