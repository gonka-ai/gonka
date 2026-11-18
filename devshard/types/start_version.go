package types

import (
	"fmt"
	"strings"
)

// StartProtocolVersion is the first non-empty MsgStartInference.protocol_version
// in diffs, with no signature check. Challengers copy it onto ChallengeReceiptRequest.
func StartProtocolVersion(diffs []Diff) string {
	for _, diff := range diffs {
		for _, tx := range diff.Txs {
			if tx == nil {
				continue
			}
			if start := tx.GetStartInference(); start != nil {
				if v := strings.TrimSpace(start.GetProtocolVersion()); v != "" {
					return v
				}
			}
		}
	}
	return ""
}

func StartProofVersion(diffs []Diff) (string, error) {
	var version string
	for _, diff := range diffs {
		for _, tx := range diff.Txs {
			if tx == nil {
				continue
			}
			start := tx.GetStartInference()
			if start == nil {
				continue
			}
			got := strings.TrimSpace(start.GetProtocolVersion())
			if got == "" {
				return "", fmt.Errorf("%w: MsgStartInference missing protocol_version", ErrStartProofMissing)
			}
			if version != "" && version != got {
				return "", fmt.Errorf("%w: %s vs %s", ErrProtocolVersionMismatch, version, got)
			}
			version = got
		}
	}
	if version == "" {
		return "", ErrStartProofMissing
	}
	return version, nil
}
