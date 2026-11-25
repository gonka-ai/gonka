package filters

import (
	"bytes"
	"errors"

	"devshard/internal/jsonshape"
)

// See README.md, "Response shape limits".
const (
	maxResponseObjectMembers  = 256
	maxStreamedChunkNodes     = 1 << 16
	minCompletionBytesPerNode = 4
)

var (
	completionObjectJSON = []byte(`"` + completionObject + `"`)
	choicesElementLimits = map[string]int{choicesField: maxIndexedElements}
)

func responseShapeLimits(payload []byte) jsonshape.Limits {
	limits := jsonshape.Limits{
		ObjectMembers:   maxResponseObjectMembers,
		RootKeyElements: choicesElementLimits,
		Nodes:           maxStreamedChunkNodes,
	}
	if bytes.Equal(jsonshape.Field(payload, objectField), completionObjectJSON) {
		limits.Nodes = max(maxStreamedChunkNodes, len(payload)/minCompletionBytesPerNode)
	}
	return limits
}

func responseShapeExceeded(payload []byte) bool {
	_, err := jsonshape.Check(payload, responseShapeLimits(payload))
	return errors.Is(err, jsonshape.ErrLimit)
}
