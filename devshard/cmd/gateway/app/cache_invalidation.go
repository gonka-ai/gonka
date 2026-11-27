package app

import (
	"devshard/cmd/gateway/nonces"
	"devshard/types"
)

type cachedReplies interface {
	InvalidateCachedReply(escrowID string, nonce uint64)
}

type verdictWatchingDiffs struct {
	diffs   nonces.DiffJournal
	replies cachedReplies
}

func (g *gateway) diffJournal() nonces.DiffJournal {
	return verdictWatchingDiffs{diffs: g.events, replies: g.replies}
}

func (watching verdictWatchingDiffs) DiffComposed(escrowID string, diff *types.Diff) {
	watching.diffs.DiffComposed(escrowID, diff)
	for _, transaction := range diff.Txs {
		if validation := transaction.GetValidation(); validation != nil && !validation.Valid {
			watching.replies.InvalidateCachedReply(escrowID, validation.InferenceId)
		}
	}
}
