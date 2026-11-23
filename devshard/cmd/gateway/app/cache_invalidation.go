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

func (w verdictWatchingDiffs) DiffComposed(escrowID string, diff *types.Diff) {
	w.diffs.DiffComposed(escrowID, diff)
	for _, tx := range diff.Txs {
		if validation := tx.GetValidation(); validation != nil && !validation.Valid {
			w.replies.InvalidateCachedReply(escrowID, validation.InferenceId)
		}
	}
}
