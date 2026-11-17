package filters

import (
	"bytes"

	json "github.com/goccy/go-json"
)

var (
	jsonNull        = []byte("null")
	jsonEmptyString = []byte(`""`)
	jsonLineEnd     = []byte("\n")
)

// answerFinish records, per choice, whether a reply that started it also finished it. See README.md, "Finishing an answer".
type answerFinish struct {
	finishedByIndex map[int64]bool
	// A host past maxIndexedElements distinct choices is not one to replay, whatever the rest of them said.
	overflowed bool
}

func (finish *answerFinish) note(choices []scannedChoice) {
	if len(choices) == 0 {
		return
	}
	if finish.finishedByIndex == nil {
		finish.finishedByIndex = make(map[int64]bool, len(choices))
	}
	for position, choice := range choices {
		index, known := numericIndex(choice.Index)
		if !known {
			index = int64(position)
		}
		terminal, seen := finish.finishedByIndex[index]
		if !seen && len(finish.finishedByIndex) >= maxIndexedElements {
			finish.overflowed = true
			continue
		}
		finish.finishedByIndex[index] = terminal || terminalReason(choice.FinishReason) || terminalReason(choice.StopReason)
	}
}

// A reply that started no choice counts as finished: an empty answer is the engine's to fault.
func (finish *answerFinish) finished() bool {
	if finish.overflowed {
		return false
	}
	for _, terminal := range finish.finishedByIndex {
		if !terminal {
			return false
		}
	}
	return true
}

// Absent, null and "" are still running.
func terminalReason(reason json.RawMessage) bool {
	return len(reason) > 0 && !bytes.Equal(reason, jsonNull) && !bytes.Equal(reason, jsonEmptyString)
}
