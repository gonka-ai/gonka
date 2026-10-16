package host

import (
	"context"
	"sync"
	"sync/atomic"
)

// coldCatchUpAttempts is how many gated walks one executor may finish on this
// verifier without making progress. A walk counts only after a catch-up page
// was answered. A verify deadline or a journal read that ends the walk before
// that answer does not count; the next walk starts again. The second answered
// walk resumes at the nonce already sent. Past the cap, checks send the
// payload alone. A check that only waited does not count.
const coldCatchUpAttempts = 2

// coldWalkLongNonces is the suffix length past which a walk from a reported
// tip takes the gate. A cold walk always takes it.
const coldWalkLongNonces = 256

// ColdWalks is one verifier's memory of journal walks, keyed by executor
// address. One gated walk runs per executor at a time. The next one starts at
// the nonce already sent. Progress is a root-matched tip above the highest one
// seen here; it clears the nonce and the count. Other verifiers keep their own.
type ColdWalks struct {
	mu    sync.Mutex
	execs map[string]*coldWalkExec
}

type coldWalkExec struct {
	running  bool
	done     chan struct{}
	sent     uint64
	attempts int
	// tip is the highest root-matched tip seen for this executor.
	tip uint64
	// bound is set when the running walk, or a check during it, saw progress.
	bound bool
	// reached is set when the executor answered a catch-up page of this walk.
	reached bool
}

// walkHead is what the first SessionHead says about the walk ahead.
type walkHead struct {
	// cold is session not found or tip 0.
	cold bool
	// matchedTip is the reported tip when its root matches the journal.
	matchedTip uint64
	// long is a suffix of more than coldWalkLongNonces.
	long bool
}

type coldJoin int

const (
	coldPass coldJoin = iota
	coldLead
	coldRetry
	coldPayloadOnly
)

type coldWalkCtx struct {
	gate *ColdWalks
	key  string
}

type coldWalkKey struct{}

// coldWalkLead is the leading check's hold on the gate. release runs once.
type coldWalkLead struct {
	gate     *ColdWalks
	key      string
	request  context.Context
	once     sync.Once
	released atomic.Bool
}

type coldLeadKey struct{}

// testBeforeColdWait, when set, runs while a check is about to wait for the
// walk already in progress. It holds neither lock.
var testBeforeColdWait func()

// WithColdWalk attaches this verifier's walk memory for one executor. A nil
// gate leaves the check unchanged.
func WithColdWalk(ctx context.Context, g *ColdWalks, executor string) context.Context {
	if g == nil {
		return ctx
	}
	return context.WithValue(ctx, coldWalkKey{}, &coldWalkCtx{gate: g, key: executor})
}

func coldWalkFrom(ctx context.Context) *coldWalkCtx {
	c, _ := ctx.Value(coldWalkKey{}).(*coldWalkCtx)
	return c
}

func coldLeadFrom(ctx context.Context) *coldWalkLead {
	l, _ := ctx.Value(coldLeadKey{}).(*coldWalkLead)
	if l == nil || l.released.Load() {
		return nil
	}
	return l
}

// NewColdWalks returns empty per-executor walk memory for one verifier.
func NewColdWalks() *ColdWalks {
	return &ColdWalks{execs: make(map[string]*coldWalkExec)}
}

func (g *ColdWalks) exec(key string) *coldWalkExec {
	st := g.execs[key]
	if st == nil {
		st = &coldWalkExec{}
		g.execs[key] = st
	}
	return st
}

// Join decides whether this check leads a gated walk, waits for one, sends
// the payload alone, or walks without the gate. retry means the walk in
// progress ended and the caller reads SessionHead again. resume is the next
// unsent nonce, or 0.
func (g *ColdWalks) Join(ctx context.Context, key string, h walkHead, latest uint64) (join coldJoin, resume uint64, err error) {
	g.mu.Lock()
	st := g.exec(key)
	progress := h.matchedTip > st.tip
	if progress {
		st.tip = h.matchedTip
		st.sent = 0
		st.attempts = 0
		if st.running {
			st.bound = true
		}
	}
	if !h.cold && !h.long {
		g.mu.Unlock()
		return coldPass, 0, nil
	}
	if !progress && (st.sent > latest || st.attempts >= coldCatchUpAttempts) {
		g.mu.Unlock()
		return coldPayloadOnly, 0, nil
	}
	if st.running {
		done := st.done
		g.mu.Unlock()
		if testBeforeColdWait != nil {
			testBeforeColdWait()
		}
		select {
		case <-done:
			return coldRetry, 0, nil
		case <-ctx.Done():
			return coldPass, 0, ctx.Err()
		}
	}
	st.running = true
	st.done = make(chan struct{})
	st.bound = progress
	st.reached = false
	resume = st.sent
	g.mu.Unlock()
	return coldLead, resume, nil
}

// Note records the next nonce the leading walk will read. It only moves forward.
func (g *ColdWalks) Note(key string, cursor uint64) {
	g.mu.Lock()
	st := g.exec(key)
	if cursor > st.sent {
		st.sent = cursor
	}
	g.mu.Unlock()
}

// Bind records a root-matched tip read during the leading walk.
func (g *ColdWalks) Bind(key string, nonce uint64) {
	g.mu.Lock()
	st := g.exec(key)
	if nonce > st.tip {
		st.tip = nonce
		st.bound = true
	}
	g.mu.Unlock()
}

// Finish ends the walk started by Join. A walk that saw progress clears the
// nonce and the count. A cancelled request keeps the nonce and spends no
// attempt. A walk that ends before the executor answers a catch-up page
// spends none either: the deadline or the journal read was this verifier's.
// Any other finish spends one.
func (g *ColdWalks) Finish(key string, requestCanceled bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.exec(key)
	if !st.running {
		return
	}
	switch {
	case st.bound:
		st.sent = 0
		st.attempts = 0
	case !requestCanceled && st.reached:
		st.attempts++
	}
	st.bound = false
	st.reached = false
	st.running = false
	close(st.done)
	st.done = nil
}

// pageAnswered records that the executor answered a catch-up page of the
// running walk. A later Finish then spends an attempt unless the walk saw
// progress or the request was cancelled.
func (g *ColdWalks) pageAnswered(key string) {
	g.mu.Lock()
	st := g.exec(key)
	if st.running {
		st.reached = true
	}
	g.mu.Unlock()
}

func (l *coldWalkLead) release() {
	l.once.Do(func() {
		l.released.Store(true)
		l.gate.Finish(l.key, l.request.Err() != nil)
	})
}

// releaseColdWalk ends the leading walk before its payload call, so a waiter
// does not wait on another inference's receipt.
func releaseColdWalk(ctx context.Context) {
	if l := coldLeadFrom(ctx); l != nil {
		l.release()
	}
}

func noteColdCursor(ctx context.Context, cursor uint64) {
	if l := coldLeadFrom(ctx); l != nil && cursor > 0 {
		l.gate.Note(l.key, cursor)
	}
}

func noteColdPageAnswered(ctx context.Context) {
	if l := coldLeadFrom(ctx); l != nil {
		l.gate.pageAnswered(l.key)
	}
}

func noteColdBound(ctx context.Context, nonce uint64) {
	if l := coldLeadFrom(ctx); l != nil && nonce > 0 {
		l.gate.Bind(l.key, nonce)
	}
}
