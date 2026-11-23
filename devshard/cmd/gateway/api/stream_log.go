package api

import (
	"cmp"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"

	"devshard/cmd/gateway/filters"
)

type streamLog struct {
	mu       sync.Mutex
	key      cacheKey
	limit    int64
	budget   *BufferBudget
	index    *streamIndex
	escrowID string

	body      []byte
	bounds    []int
	charged   int64
	hasSocket bool
	reading   bool
	done      bool
	dropped   bool
	changed   chan struct{}
}

func newStreamLog(key cacheKey, limit int64, budget *BufferBudget, index *streamIndex) *streamLog {
	log := &streamLog{
		key: key, limit: limit, budget: budget, index: index,
		bounds:    make([]int, 0, expectedStreamEvents),
		hasSocket: true,
		changed:   make(chan struct{}),
	}
	index.add(log)
	return log
}

func (l *streamLog) noteEscrow(escrowID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.escrowID = escrowID
}

func (l *streamLog) escrow() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.escrowID
}

func (l *streamLog) socketAttached() bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hasSocket
}

func (l *streamLog) detachSocket() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hasSocket = false
}

func (l *streamLog) append(data []byte) (socketAttached bool) {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.done {
		l.appendLocked(data)
	}
	return l.hasSocket
}

func (l *streamLog) appendLocked(data []byte) {
	size := int64(len(data))
	if int64(len(l.body))+size > l.limit || !l.budget.reserve(size) {
		l.dropLocked()
		return
	}
	l.charged += size
	l.body = append(l.body, data...)
	l.bounds = append(l.bounds, len(l.body))
	l.wakeLocked()
}

func (l *streamLog) dropLocked() {
	l.done, l.dropped = true, true
	l.body, l.bounds = nil, nil
	l.releaseBudgetLocked()
	l.index.remove(l)
	l.wakeLocked()
}

func (l *streamLog) releaseBudgetLocked() {
	l.budget.release(l.charged)
	l.charged = 0
}

func (l *streamLog) wakeLocked() {
	close(l.changed)
	l.changed = make(chan struct{})
}

func (l *streamLog) conclude(escrowID string, unstorable error) (cachedResponse, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dropped {
		return cachedResponse{}, cacheRefusedTooLarge
	}
	escrowID = cmp.Or(escrowID, l.escrowID)
	l.done = true
	l.releaseBudgetLocked()
	l.wakeLocked()
	if refusal := replayRefusal(unstorable, len(l.body), escrowID); refusal != filters.CacheStorable {
		return cachedResponse{}, refusal
	}
	return cachedResponse{
		escrowID:    escrowID,
		stream:      true,
		status:      http.StatusOK,
		contentType: contentTypeEventStream,
		body:        exactly(l.body),
		bounds:      exactly(l.bounds),
	}, filters.CacheStorable
}

type logReader struct {
	log  *streamLog
	sent int
}

func (l *streamLog) adopt() (*logReader, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hasSocket || l.reading || l.dropped {
		return nil, false
	}
	l.reading = true
	return &logReader{log: l}, true
}

func (r *logReader) next() (events []byte, finished bool, changed <-chan struct{}) {
	l := r.log
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.sent < len(l.body) {
		events = l.body[r.sent:]
		r.sent = len(l.body)
		return events, false, nil
	}
	return nil, l.done, l.changed
}

func (r *logReader) dropped() bool {
	r.log.mu.Lock()
	defer r.log.mu.Unlock()
	return r.log.dropped
}

func (r *logReader) release() {
	r.log.mu.Lock()
	defer r.log.mu.Unlock()
	r.log.reading = false
}

type streamIndex struct {
	mu      sync.Mutex
	logs    map[cacheKey][]*streamLog
	replays atomic.Int64
}

func newStreamIndex() *streamIndex { return &streamIndex{logs: make(map[cacheKey][]*streamLog)} }

func (index *streamIndex) add(log *streamLog) {
	index.mu.Lock()
	defer index.mu.Unlock()
	index.logs[log.key] = append(index.logs[log.key], log)
}

func (index *streamIndex) remove(log *streamLog) {
	index.mu.Lock()
	defer index.mu.Unlock()
	remaining := slices.DeleteFunc(index.logs[log.key], func(held *streamLog) bool { return held == log })
	if len(remaining) == 0 {
		delete(index.logs, log.key)
		return
	}
	index.logs[log.key] = remaining
}

func (index *streamIndex) adopt(key cacheKey) (*logReader, bool) {
	index.mu.Lock()
	candidates := slices.Clone(index.logs[key])
	index.mu.Unlock()
	for _, log := range candidates {
		if reader, adopted := log.adopt(); adopted {
			index.replays.Add(1)
			return reader, true
		}
	}
	return nil, false
}

func (index *streamIndex) stats() (replays, live int64) {
	index.mu.Lock()
	defer index.mu.Unlock()
	for _, held := range index.logs {
		live += int64(len(held))
	}
	return index.replays.Load(), live
}
