package host

import (
	"google.golang.org/protobuf/proto"

	"devshard/storage"
	"devshard/types"
)

const (
	// ChallengePageMaxBytes is the most wire-allowance bytes one challenge
	// page may carry. A single diff larger than this is a page by itself.
	ChallengePageMaxBytes = 4 << 20
	// ChallengePageMaxNonces bounds one DiffSizes read and one request body.
	ChallengePageMaxNonces = 8192
	// ChallengeDiffWireOverhead is the allowance, per diff, for the nonce,
	// user signature, post-state root, and proto tags on top of txs_proto.
	ChallengeDiffWireOverhead = 128
)

// PackChallengePage returns how many leading sizes form one challenge page.
// The first entry is always taken, so an oversized diff is a page by itself.
// sizes must be contiguous; a hole stops the page before it.
func PackChallengePage(sizes []storage.DiffSize) int {
	total := 0
	for i, s := range sizes {
		if i == ChallengePageMaxNonces {
			return i
		}
		cost := s.Bytes + ChallengeDiffWireOverhead
		if i > 0 && (s.Nonce != sizes[i-1].Nonce+1 || total+cost > ChallengePageMaxBytes) {
			return i
		}
		total += cost
	}
	return len(sizes)
}

func challengePageCost(diffs []types.Diff) int {
	total := 0
	for _, d := range diffs {
		total += proto.Size(&types.DiffContent{Txs: d.Txs}) + ChallengeDiffWireOverhead
	}
	return total
}

// pageFitsPayload reports that page plus the prompt still fit one challenge
// page. An empty page is the payload-only call. A single diff larger than
// the budget goes alone.
func pageFitsPayload(page []types.Diff, payload *InferencePayload) bool {
	if len(page) == 0 {
		return true
	}
	cost := challengePageCost(page)
	if len(page) == 1 && cost > ChallengePageMaxBytes {
		return false
	}
	extra := 0
	if payload != nil {
		extra = len(payload.Prompt)
	}
	return cost+extra <= ChallengePageMaxBytes
}
