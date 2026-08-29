package user

import (
	"context"
	"strings"
	"sync"
	"time"

	"devshard/heightsync"
	"devshard/logging"
	"devshard/transport"
)

// heightSyncSeeder is implemented by transport.HTTPClient. In-process clients
// omit it, so the cold-start seed (spec §18.5) is a no-op there.
type heightSyncSeeder interface {
	SeedHeightSync(ctx context.Context) (ok bool, err error)
}

const (
	// defaultHeightSeedRetryDeadline is how long a session waits for the
	// versioned route to serve POST /height-sync (catalog admission, host
	// boot). After this, seed degrades to omit.
	defaultHeightSeedRetryDeadline = 2 * time.Minute
	heightSeedRetryInitial         = 50 * time.Millisecond
	// inlineHeightSeedBudget bounds the seed when it runs on the inference
	// path. A miss only degrades to the unstamped-nonce-1 path, so a chat must
	// never wait out the full catalog-admission deadline.
	inlineHeightSeedBudget = 2 * time.Second
)

// SeedHeightSync fans POST /sessions/:id/height-sync across the roster.
// Success is at least half of unique seed targets returning an Anchor.
// Retryable misses (undeclared-version 503, 429, transient dial) retry only
// the still-unseeded slots until that quorum, a terminal miss, or the
// deadline. Once quorum is met, one more fan-out collects Anchors from every
// slot (including ones already seeded). Each RPC is one-shot: transport does
// not nest its 5s 429/503 retry under this loop. Safe to call repeatedly;
// later calls no-op once a terminal outcome is recorded. Never returns an
// error: a miss degrades to today's unstamped-nonce-1 path.
func (s *Session) SeedHeightSync(ctx context.Context) {
	s.ensureHeightSeed(ctx)
}

// HeightSeedMissed reports whether the cold-start seed ran and did not collect
// Anchors from at least half of the unique seed targets. Tests only.
func (s *Session) HeightSeedMissed() bool {
	return s.heightSeedMissed.Load()
}

func (s *Session) ensureHeightSeed(ctx context.Context) {
	if s == nil || s.heightSeedDone.Load() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.heightSeedMu.Lock()
	defer s.heightSeedMu.Unlock()
	if s.heightSeedDone.Load() {
		return
	}
	s.runHeightSeed(ctx, s.heightSeedDeadlineLocked(ctx))
}

// ensureHeightSeedInline is the inference-path form. It gives the seed a short
// budget and never queues behind a seed another goroutine already owns, so a
// cold gateway waiting on catalog admission does not stall chat: the heartbeat
// loop or a later caller resumes the retry until the overall deadline.
func (s *Session) ensureHeightSeedInline() {
	if s == nil || s.heightSeedDone.Load() {
		return
	}
	if !s.heightSeedMu.TryLock() {
		return
	}
	defer s.heightSeedMu.Unlock()
	if s.heightSeedDone.Load() {
		return
	}
	overall := s.heightSeedDeadlineLocked(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), inlineHeightSeedBudget)
	defer cancel()
	s.runHeightSeed(ctx, overall)
}

// heightSeedDeadlineLocked pins the overall retry deadline on the first
// attempt. Later callers inherit it rather than extending or truncating it.
// Caller holds heightSeedMu.
func (s *Session) heightSeedDeadlineLocked(ctx context.Context) time.Time {
	if pinned := s.heightSeedUntil.Load(); pinned != 0 {
		return time.Unix(0, pinned)
	}
	dl := heightSeedDeadline(ctx)
	s.heightSeedUntil.Store(dl.UnixNano())
	return dl
}

func heightSeedDeadline(ctx context.Context) time.Time {
	if dl, ok := ctx.Deadline(); ok {
		return dl
	}
	return time.Now().Add(defaultHeightSeedRetryDeadline)
}

// heightSeedQuorum is true when at least half of slots returned an Anchor
// (seeded*2 >= slots). One slot still requires that one Anchor; two slots
// succeed on one; three require two.
func heightSeedQuorum(seeded, slots int) bool {
	return slots > 0 && seeded*2 >= slots
}

// runHeightSeed fans the seed out until quorum, a terminal miss, or deadline.
// ctx bounds this call; deadline bounds the seed as a whole. When ctx runs out
// first and the misses are still retryable, the seed is left unfinished.
func (s *Session) runHeightSeed(ctx context.Context, deadline time.Time) {
	s.mu.Lock()
	// Test-injected height sources already provide a stamp; do not hit the wire.
	if s.observedHeight != nil {
		s.mu.Unlock()
		s.heightSeedDone.Store(true)
		return
	}
	clients := append([]HostClient(nil), s.clients...)
	escrow := s.escrowID
	nonceBefore := s.nonce
	hLastBefore := uint64(0)
	if s.turnTracker != nil {
		hLastBefore = s.turnTracker.LastCompletedHeight()
	}
	s.mu.Unlock()

	seen := make(map[heightSyncSeeder]struct{})
	var targets []seedTarget
	for i, c := range clients {
		seeder, ok := c.(heightSyncSeeder)
		if !ok || seeder == nil {
			continue
		}
		if _, dup := seen[seeder]; dup {
			continue
		}
		seen[seeder] = struct{}{}
		targets = append(targets, seedTarget{slot: i, seeder: seeder})
	}
	if len(targets) == 0 {
		s.heightSeedDone.Store(true)
		return
	}
	if s.heightSeedOK == nil {
		s.heightSeedOK = make(map[int]struct{})
	}

	logMutation := func() {
		s.mu.Lock()
		nonceAfter := s.nonce
		hLastAfter := uint64(0)
		if s.turnTracker != nil {
			hLastAfter = s.turnTracker.LastCompletedHeight()
		}
		s.mu.Unlock()
		if nonceAfter != nonceBefore || hLastAfter != hLastBefore {
			logging.Warn("heightsync: seed mutated log state",
				heightsync.LogFieldSubsystem, "heightsync",
				"escrow", escrow,
				"nonce_before", nonceBefore,
				"nonce_after", nonceAfter,
				"h_last_before", hLastBefore,
				"h_last_after", hLastAfter)
		}
	}
	observedHeight := func() uint64 {
		s.mu.Lock()
		defer s.mu.Unlock()
		h, _, _ := s.observedHeightLocked()
		return h
	}
	finishOK := func() {
		s.heightSeedDone.Store(true)
		logging.Info("heightsync: seed_ok",
			heightsync.LogFieldSubsystem, "heightsync",
			"escrow", escrow,
			"seeded", len(s.heightSeedOK),
			"slots", len(targets),
			heightsync.LogFieldHeight, observedHeight(),
		)
	}
	sweepAll := func() {
		applySeedOutcomes(escrow, s.heightSeedOK, fanHeightSeed(ctx, targets))
		logMutation()
		finishOK()
	}

	if heightSeedQuorum(len(s.heightSeedOK), len(targets)) {
		if ctx.Err() != nil {
			return
		}
		sweepAll()
		return
	}

	pending := unseededHeightTargets(targets, s.heightSeedOK)
	delay := heightSeedRetryInitial
	for {
		if len(pending) == 0 {
			break
		}

		out := fanHeightSeed(ctx, pending)
		retryable, misses := applySeedOutcomes(escrow, s.heightSeedOK, out)
		logMutation()

		if heightSeedQuorum(len(s.heightSeedOK), len(targets)) {
			if ctx.Err() != nil {
				return
			}
			sweepAll()
			return
		}

		pending = retryableHeightTargets(targets, out)
		remaining := time.Until(deadline)
		if retryable && len(pending) > 0 && remaining > 0 && ctx.Err() == nil {
			sleep := delay
			if sleep > remaining {
				sleep = remaining
			}
			logging.Debug("heightsync: seed retry",
				heightsync.LogFieldSubsystem, "heightsync",
				"escrow", escrow,
				"sleep", sleep.String(),
				"seeded", len(s.heightSeedOK),
				"slots", len(targets),
				"pending", len(pending),
				"misses", strings.Join(misses, ";"),
			)
			timer := time.NewTimer(sleep)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
			if ctx.Err() == nil && time.Now().Before(deadline) {
				if delay < 5*time.Second {
					delay *= 2
				}
				continue
			}
		}

		// This caller's budget expired but the seed's own deadline has not, and
		// the misses are still retryable (catalog admission still pending). Leave the
		// seed unfinished so the next caller resumes it.
		if retryable && len(pending) > 0 && time.Now().Before(deadline) {
			logging.Debug("heightsync: seed budget elapsed, will resume",
				heightsync.LogFieldSubsystem, "heightsync",
				"escrow", escrow,
				"seeded", len(s.heightSeedOK),
				"slots", len(targets),
				"pending", len(pending),
				"misses", strings.Join(misses, ";"),
			)
			return
		}

		break
	}

	s.heightSeedMissed.Store(true)
	s.heightSeedDone.Store(true)
	logging.Info("heightsync: seed_missed",
		heightsync.LogFieldSubsystem, "heightsync",
		"escrow", escrow,
		"seeded", len(s.heightSeedOK),
		"slots", len(targets),
	)
}

type seedTarget struct {
	slot   int
	seeder heightSyncSeeder
}

type seedOutcome struct {
	slot int
	ok   bool
	err  error
}

func fanHeightSeed(ctx context.Context, targets []seedTarget) []seedOutcome {
	out := make([]seedOutcome, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t seedTarget) {
			defer wg.Done()
			ok, err := t.seeder.SeedHeightSync(ctx)
			out[i] = seedOutcome{slot: t.slot, ok: ok, err: err}
		}(i, t)
	}
	wg.Wait()
	return out
}

func applySeedOutcomes(escrow string, okSlots map[int]struct{}, out []seedOutcome) (retryable bool, misses []string) {
	for _, r := range out {
		if r.ok {
			okSlots[r.slot] = struct{}{}
			continue
		}
		reason := "omit"
		if r.err != nil {
			reason = r.err.Error()
			if transport.IsRetryableNonInference(r.err) {
				retryable = true
			}
		}
		misses = append(misses, strings.TrimSpace(reason))
		logging.Debug("heightsync: seed slot miss",
			heightsync.LogFieldSubsystem, "heightsync",
			"escrow", escrow,
			"slot", r.slot,
			"error", reason)
	}
	return retryable, misses
}

func unseededHeightTargets(targets []seedTarget, okSlots map[int]struct{}) []seedTarget {
	var pending []seedTarget
	for _, t := range targets {
		if _, ok := okSlots[t.slot]; !ok {
			pending = append(pending, t)
		}
	}
	return pending
}

func retryableHeightTargets(targets []seedTarget, out []seedOutcome) []seedTarget {
	bySlot := make(map[int]seedTarget, len(targets))
	for _, t := range targets {
		bySlot[t.slot] = t
	}
	var pending []seedTarget
	for _, r := range out {
		if r.ok || r.err == nil || !transport.IsRetryableNonInference(r.err) {
			continue
		}
		if t, ok := bySlot[r.slot]; ok {
			pending = append(pending, t)
		}
	}
	return pending
}
