package user

import (
	"context"
	"strings"
	"sync"

	"devshard/heightsync"
	"devshard/logging"
)

// heightSyncSeeder is implemented by transport.HTTPClient. In-process clients
// omit it, so E9 is a no-op there.
type heightSyncSeeder interface {
	SeedHeightSync(ctx context.Context) (ok bool, err error)
}

// SeedHeightSync fans POST /sessions/:id/height-sync across the roster once
// (plan §8.5.1 / E9). Safe to call repeatedly; later calls no-op. Never
// returns an error: a total miss degrades to today's unstamped-nonce-1 path.
func (s *Session) SeedHeightSync(ctx context.Context) {
	s.ensureHeightSeed(ctx)
}

// HeightSeedMissed reports whether the E9 round ran and collected no Anchor.
// Tests only (H36).
func (s *Session) HeightSeedMissed() bool {
	return s.heightSeedMissed.Load()
}

func (s *Session) ensureHeightSeed(ctx context.Context) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.heightSeedOnce.Do(func() { s.runHeightSeed(ctx) })
}

func (s *Session) runHeightSeed(ctx context.Context) {
	s.mu.Lock()
	// Test-injected height sources already provide a stamp; do not hit the wire.
	if s.observedHeight != nil {
		s.mu.Unlock()
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
		return
	}

	type seedOutcome struct {
		slot int
		ok   bool
		err  error
	}
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
	var misses []string
	for _, r := range out {
		if r.ok {
			seeded++
			continue
		}
		reason := "omit"
		if r.err != nil {
			reason = r.err.Error()
		}
		misses = append(misses, strings.TrimSpace(reason))
		logging.Debug("heightsync: seed slot miss",
			heightsync.LogFieldSubsystem, "heightsync",
			"escrow", escrow,
			"slot", r.slot,
			"error", reason)
	}

	s.mu.Lock()
	hNow, _, have := s.observedHeightLocked()
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

	if seeded == 0 && !have {
		s.heightSeedMissed.Store(true)
		logging.Info("heightsync: seed_missed",
			heightsync.LogFieldSubsystem, "heightsync",
			"escrow", escrow,
			"slots", len(targets),
			"misses", strings.Join(misses, ";"),
		)
		return
	}
	logging.Info("heightsync: seed_ok",
		heightsync.LogFieldSubsystem, "heightsync",
		"escrow", escrow,
		"seeded", seeded,
		"slots", len(targets),
		heightsync.LogFieldHeight, hNow,
	)
}
