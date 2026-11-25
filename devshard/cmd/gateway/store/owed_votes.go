package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// OwedVote is one refusal vote a race owes, with the prompt the hosts check that vote against. See README.md, "Owed refusal votes".
type OwedVote struct {
	EscrowID    string
	Nonce       uint64
	StartedAt   time.Time
	RequestID   string
	Participant string
	Model       string
	Prompt      []byte
}

const (
	owedVoteQueueDepth     = 1024
	maxOwedVotePromptBytes = 1 << 20
)

// OwedVoteStats counts the saves and deletes the writer shed or the database refused.
type OwedVoteStats struct {
	Dropped int64
	Failed  int64
}

type owedVoteWrite struct {
	vote OwedVote
	paid bool
}

// OwedVotes writes from one goroutine and sheds a write rather than block: a race owes its votes on the response path.
type OwedVotes struct {
	store *Store
	queue chan owedVoteWrite
	done  chan struct{}

	mu     sync.RWMutex
	closed bool

	dropped atomic.Int64
	failed  atomic.Int64
}

// NewOwedVotes registers the writer with the store, whose Close drains it before the database handle goes.
func (s *Store) NewOwedVotes() (*OwedVotes, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owedVotes != nil && !s.owedVotes.isClosed() {
		return nil, errors.New("owed votes: already open")
	}
	owedVotes := &OwedVotes{
		store: s,
		queue: make(chan owedVoteWrite, owedVoteQueueDepth),
		done:  make(chan struct{}),
	}
	s.owedVotes = owedVotes
	go owedVotes.run()
	return owedVotes, nil
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

func (o *OwedVotes) Stats() OwedVoteStats {
	return OwedVoteStats{Dropped: o.dropped.Load(), Failed: o.failed.Load()}
}

// Load returns every vote a previous process owed and did not post, ordered by (escrow_id, nonce).
func (o *OwedVotes) Load(ctx context.Context) ([]OwedVote, error) {
	rows, err := o.store.db.QueryContext(ctx, `
		SELECT escrow_id, nonce, started_at, request_id, participant, model, prompt
		FROM owed_refusal_votes ORDER BY escrow_id, nonce`)
	if err != nil {
		return nil, fmt.Errorf("loading owed votes: %w", err)
	}
	defer rows.Close()
	var votes []OwedVote
	for rows.Next() {
		var vote OwedVote
		var startedAt string
		if err := rows.Scan(&vote.EscrowID, &vote.Nonce, &startedAt, &vote.RequestID, &vote.Participant, &vote.Model, &vote.Prompt); err != nil {
			return nil, fmt.Errorf("scanning owed vote row: %w", err)
		}
		if vote.StartedAt, err = parseTime(startedAt); err != nil {
			return nil, fmt.Errorf("parsing owed vote started_at %q: %w", startedAt, err)
		}
		votes = append(votes, vote)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating owed votes: %w", err)
	}
	return votes, nil
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
		if err := o.write(write); err != nil {
			o.failed.Add(1)
		}
	}
}

func (o *OwedVotes) write(write owedVoteWrite) error {
	if write.paid {
		_, err := o.store.db.Exec(`DELETE FROM owed_refusal_votes WHERE escrow_id = ? AND nonce = ?`, write.vote.EscrowID, write.vote.Nonce)
		return err
	}
	vote := write.vote
	_, err := o.store.db.Exec(`
		INSERT INTO owed_refusal_votes (escrow_id, nonce, started_at, request_id, participant, model, prompt)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(escrow_id, nonce) DO NOTHING`,
		vote.EscrowID, vote.Nonce, FormatTime(vote.StartedAt), vote.RequestID, vote.Participant, vote.Model, vote.Prompt)
	return err
}
