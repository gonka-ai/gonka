package transport

import (
	"fmt"

	json "github.com/goccy/go-json"

	"devshard/types"
)

type startProofBody struct {
	ProtocolVersion string     `json:"protocol_version"`
	Diffs           []DiffJSON `json:"diffs"`
}

// PeekStartProof extracts creator-signed start diffs and an optional claimed
// protocol_version from a challenge / verify JSON body. Unknown shapes return
// empty diffs (caller treats that as no proof).
func PeekStartProof(body []byte) (diffs []types.Diff, protocolVersion string, err error) {
	if len(body) == 0 {
		return nil, "", nil
	}
	var wrap startProofBody
	if err := json.Unmarshal(body, &wrap); err != nil {
		return nil, "", err
	}
	if len(wrap.Diffs) == 0 {
		return nil, wrap.ProtocolVersion, nil
	}
	diffs = make([]types.Diff, 0, len(wrap.Diffs))
	for i, dj := range wrap.Diffs {
		d, dErr := DiffFromJSON(dj)
		if dErr != nil {
			return nil, "", fmt.Errorf("decode diff %d: %w", i, dErr)
		}
		diffs = append(diffs, d)
	}
	return diffs, wrap.ProtocolVersion, nil
}
