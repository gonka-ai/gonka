package store

import (
	"context"
	"sync"
	"sync/atomic"
)

type owedVoteWrite struct {
	vote OwedVote
	paid bool
}

// OwedVotes writes from one goroutine and sheds a write rather than block: a race owes its votes on the response path.
type OwedVotes struct {
	rows  OwedVoteRows
	queue chan owedVoteWrite
	done  chan struct{}

	mu     sync.RWMutex
	closed bool

	dropped atomic.Int64
	failed  atomic.Int64
}

func newOwedVotes(rows OwedVoteRows) *OwedVotes {
	return &OwedVotes{
		rows:  rows,
		queue: make(chan owedVoteWrite, owedVoteQueueDepth),
		done:  make(chan struct{}),
	}
}

// Owe queues the vote's row; a prompt over the bound, a full queue or a closed writer leaves the vote in memory only.
func (o *OwedVotes) Owe(vote OwedVote) {
	if len(vote.Prompt) > maxOwedVotePromptBytes {
		return
	}
	o.enqueue(owedVoteWrite{vote: vote})
}

// Paid queues the delete of a vote's row; a shed delete leaves a row the next boot replays and drops.
func (o *OwedVotes) Paid(escrowID string, nonce uint64) {
	o.enqueue(owedVoteWrite{vote: OwedVote{EscrowID: escrowID, Nonce: nonce}, paid: true})
}

func (o *OwedVotes) enqueue(write owedVoteWrite) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.closed {
		o.dropped.Add(1)
		return
	}
	select {
	case o.queue <- write:
	default:
		o.dropped.Add(1)
	}
}

// Stats returns the counters of what the writer has shed or lost so far.
func (o *OwedVotes) Stats() OwedVoteStats {
	return OwedVoteStats{Dropped: o.dropped.Load(), Failed: o.failed.Load()}
}

// Load returns every vote a previous process owed and did not post, ordered by (escrow_id, nonce).
func (o *OwedVotes) Load(ctx context.Context) ([]OwedVote, error) {
	return o.rows.LoadOwedVotes(ctx)
}

func (o *OwedVotes) drain() {
	o.mu.Lock()
	alreadyClosed := o.closed
	o.closed = true
	o.mu.Unlock()
	if alreadyClosed {
		return
	}
	close(o.queue)
	<-o.done
}

func (o *OwedVotes) isClosed() bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.closed
}

func (o *OwedVotes) run() {
	defer close(o.done)
	for write := range o.queue {
		if err := o.write(context.Background(), write); err != nil {
			o.failed.Add(1)
		}
	}
}

func (o *OwedVotes) write(ctx context.Context, write owedVoteWrite) error {
	if write.paid {
		return o.rows.DeleteOwedVote(ctx, write.vote.EscrowID, write.vote.Nonce)
	}
	return o.rows.SaveOwedVote(ctx, write.vote)
}
