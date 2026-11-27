package store

import (
	"context"
	"errors"
	"sync"
	"time"
)

var errRefused = errors.New("refused")

type fakeBackend struct {
	Backend

	mu             sync.Mutex
	events         []string
	inserted       []RequestRecord
	savedVotes     []OwedVote
	deletedVotes   int
	deleteBefore   []time.Time
	deleteBeyond   []int64
	insertErr      error
	sweepBeforeErr error
	sweepBeyondErr error
	voteErr        error
	hold           chan struct{}
}

func (f *fakeBackend) wait() {
	if f.hold != nil {
		<-f.hold
	}
}

func (f *fakeBackend) log(event string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
}

func (f *fakeBackend) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func (f *fakeBackend) InsertRequest(_ context.Context, record RequestRecord) error {
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "insert")
	if f.insertErr != nil {
		return f.insertErr
	}
	f.inserted = append(f.inserted, record)
	return nil
}

func (f *fakeBackend) DeleteRequestsBefore(_ context.Context, cutoff time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteBefore = append(f.deleteBefore, cutoff)
	return f.sweepBeforeErr
}

func (f *fakeBackend) DeleteRequestsBeyond(_ context.Context, keepRows int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteBeyond = append(f.deleteBeyond, keepRows)
	return f.sweepBeyondErr
}

func (f *fakeBackend) SaveOwedVote(_ context.Context, vote OwedVote) error {
	f.wait()
	f.log("save vote")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.voteErr != nil {
		return f.voteErr
	}
	f.savedVotes = append(f.savedVotes, vote)
	return nil
}

func (f *fakeBackend) DeleteOwedVote(_ context.Context, _ string, _ uint64) error {
	f.wait()
	f.log("delete vote")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.voteErr != nil {
		return f.voteErr
	}
	f.deletedVotes++
	return nil
}

func (f *fakeBackend) Close() error {
	f.log("close")
	return nil
}
