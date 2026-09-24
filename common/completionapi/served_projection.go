package completionapi

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"hash"
	"strings"
)

const doneLine = DataPrefix + "[DONE]"

func StripForGateway(stored []byte) ([]byte, error) {
	document, err := decodeJSONDocument(stored)
	if err != nil {
		return nil, err
	}
	if events, isEnvelope := streamedEnvelopeEvents(document); isEnvelope {
		served := make([]string, 0, len(events))
		for _, raw := range events {
			if line, isString := raw.(string); isString {
				served = append(served, stripStreamedLine(line))
			}
		}
		return json.Marshal(SerializedStreamedResponse{Events: served})
	}
	if _, isObject := document.(map[string]any); !isObject {
		return nil, errors.New("strip for gateway: stored payload is not a JSON object")
	}
	dropFields(document, fieldsOnlyAskingCallersSee)
	return json.Marshal(document)
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

type envelopeHasher struct {
	digest    hash.Hash
	lineCount int
	sum       *[32]byte
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

type ReceivedResponseHasher struct {
	envelope  *envelopeHasher
	firstBody string
	bareBody  bool
}

func NewReceivedResponseHasher() *ReceivedResponseHasher {
	return &ReceivedResponseHasher{envelope: newEnvelopeHasher()}
}

func (hasher *ReceivedResponseHasher) Add(line string) {
	if line == "" {
		return
	}
	switch {
	case hasher.envelope.lineCount == 0:
		hasher.firstBody = strings.TrimPrefix(line, DataPrefix)
		hasher.bareBody = strings.HasPrefix(line, DataPrefix) && line != doneLine
	case hasher.envelope.lineCount > 1 || line != doneLine:
		hasher.bareBody = false
		hasher.firstBody = ""
	}
	hasher.envelope.add(line)
}

func (hasher *ReceivedResponseHasher) Sums() [][32]byte {
	if hasher.envelope.lineCount == 0 {
		return nil
	}
	sums := [][32]byte{hasher.envelope.finish()}
	if hasher.bareBody {
		sums = append(sums, sha256.Sum256([]byte(hasher.firstBody)))
	}
	return sums
}
