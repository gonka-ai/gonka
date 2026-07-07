package public

import (
	"testing"
	"time"
)

func TestSnapshotCountLimiter(t *testing.T) {
	t.Run("caps distinct counts, allows repeats", func(t *testing.T) {
		l := newSnapshotCountLimiter()

		for i, count := range []uint32{100, 200, 300} {
			if !l.Allow("val1", 1000, "m1", count) {
				t.Fatalf("distinct count %d (#%d) should be allowed", count, i+1)
			}
		}
		if l.Allow("val1", 1000, "m1", 400) {
			t.Fatal("4th distinct count should be rejected")
		}
		// Repeats of already-seen counts stay allowed.
		for _, count := range []uint32{100, 200, 300} {
			if !l.Allow("val1", 1000, "m1", count) {
				t.Fatalf("repeat of count %d should be allowed", count)
			}
		}
		// Still rejected after repeats.
		if l.Allow("val1", 1000, "m1", 500) {
			t.Fatal("new distinct count should stay rejected")
		}
	})

	t.Run("quota is per validator, stage, and model", func(t *testing.T) {
		l := newSnapshotCountLimiter()
		for _, count := range []uint32{1, 2, 3} {
			l.Allow("val1", 1000, "m1", count)
		}
		if !l.Allow("val2", 1000, "m1", 4) {
			t.Fatal("other validator must have its own quota")
		}
		if !l.Allow("val1", 2000, "m1", 4) {
			t.Fatal("other stage must have its own quota")
		}
		if !l.Allow("val1", 1000, "m2", 4) {
			t.Fatal("other model must have its own quota")
		}
	})

	t.Run("idle entries expire", func(t *testing.T) {
		l := newSnapshotCountLimiter()
		current := time.Unix(0, 0)
		l.now = func() time.Time { return current }

		for _, count := range []uint32{1, 2, 3} {
			l.Allow("val1", 1000, "m1", count)
		}
		if l.Allow("val1", 1000, "m1", 4) {
			t.Fatal("quota should be exhausted")
		}

		current = current.Add(snapshotLimiterIdleTTL + time.Minute)
		if !l.Allow("val1", 1000, "m1", 4) {
			t.Fatal("expired entry should reset the quota")
		}
	})
}
