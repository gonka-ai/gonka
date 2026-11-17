package types

import "strings"

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
