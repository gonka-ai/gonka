package user

import (
	"context"
	"errors"
	"net/http"
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
)

// SeedHeightSync fans POST /sessions/:id/height-sync across the roster.
// Success is at least half of unique seed targets returning an Anchor.
// Retryable misses (undeclared-version 503, 429, transient dial) retry until
// that quorum, a terminal miss, or the deadline. Safe to call repeatedly;
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
	s.runHeightSeed(ctx)
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

func isHeightSeedRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var status *transport.UpstreamStatusError
	if errors.As(err, &status) {
		if transport.IsUndeclaredVersionError(status.Body) {
			return true
		}
		return status.StatusCode == http.StatusTooManyRequests ||
			status.StatusCode == http.StatusServiceUnavailable
	}
	return transport.IsTransientWriteError(err)
}

func (s *Session) runHeightSeed(ctx context.Context) {
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

	type seedTarget struct {
		slot   int
		seeder heightSyncSeeder
	}
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

	type seedOutcome struct {
		slot int
		ok   bool
		err  error
	}

	deadline := heightSeedDeadline(ctx)
	delay := heightSeedRetryInitial
	for {
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

		seeded := 0
		retryable := false
		var misses []string
		for _, r := range out {
			if r.ok {
				seeded++
				continue
			}
			reason := "omit"
			if r.err != nil {
				reason = r.err.Error()
				if isHeightSeedRetryable(r.err) {
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

		s.mu.Lock()
		hNow, _, _ := s.observedHeightLocked()
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

		if heightSeedQuorum(seeded, len(targets)) {
			s.heightSeedDone.Store(true)
			logging.Info("heightsync: seed_ok",
				heightsync.LogFieldSubsystem, "heightsync",
				"escrow", escrow,
				"seeded", seeded,
				"slots", len(targets),
				heightsync.LogFieldHeight, hNow,
			)
			return
		}

		remaining := time.Until(deadline)
		if retryable && remaining > 0 && ctx.Err() == nil {
			sleep := delay
			if sleep > remaining {
				sleep = remaining
			}
			logging.Debug("heightsync: seed retry",
				heightsync.LogFieldSubsystem, "heightsync",
				"escrow", escrow,
				"sleep", sleep.String(),
				"seeded", seeded,
				"slots", len(targets),
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

		s.heightSeedMissed.Store(true)
		s.heightSeedDone.Store(true)
		logging.Info("heightsync: seed_missed",
			heightsync.LogFieldSubsystem, "heightsync",
			"escrow", escrow,
			"seeded", seeded,
			"slots", len(targets),
			"misses", strings.Join(misses, ";"),
		)
		return
	}
}
