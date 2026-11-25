package filters

import (
	"bytes"
	"errors"

	"devshard/internal/jsonshape"
)

// See README.md, "Response shape limits".
const (
	maxResponseObjectMembers = 256
	maxStreamedChunkNodes    = 1 << 16

	choicesField = "choices"
	objectField  = "object"
)

var (
	completionObjectJSON = []byte(`"` + completionObject + `"`)

	streamedChunkShapeLimits = jsonshape.Limits{
		ObjectMembers:   maxResponseObjectMembers,
		RootKeyElements: map[string]int{choicesField: maxIndexedElements},
		Nodes:           maxStreamedChunkNodes,
	}
	completionShapeLimits = jsonshape.Limits{
		ObjectMembers:   maxResponseObjectMembers,
		RootKeyElements: map[string]int{choicesField: maxIndexedElements},
	}
)

func responseShapeLimits(payload []byte) jsonshape.Limits {
	if bytes.Equal(jsonshape.FieldFold(payload, objectField), completionObjectJSON) {
		return completionShapeLimits
	}
	return streamedChunkShapeLimits
}

func responseShapeExceeded(payload []byte) bool {
	_, err := jsonshape.Check(payload, responseShapeLimits(payload))
	return errors.Is(err, jsonshape.ErrLimit)
}
