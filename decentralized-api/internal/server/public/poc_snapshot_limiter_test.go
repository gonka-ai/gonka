package public

import (
	"testing"
	"time"
)

func TestSnapshotCountLimiter(t *testing.T) {
	t.Run("caps distinct counts, allows repeats", func(t *testing.T) {
		l := newSnapshotCountLimiter()

		for i, count := range []uint32{100, 200, 300} {
			allowed, distinct := l.Allow("val1", 1000, "m1", count)
			if !allowed {
				t.Fatalf("distinct count %d (#%d) should be allowed", count, i+1)
			}
			if distinct != i+1 {
				t.Fatalf("distinct = %d, want %d", distinct, i+1)
			}
		}
		if allowed, distinct := l.Allow("val1", 1000, "m1", 400); allowed {
			t.Fatal("4th distinct count should be rejected")
		} else if distinct != 4 {
			t.Fatalf("rejected distinct = %d, want 4", distinct)
		}
		// Repeats of already-seen counts stay allowed.
		for _, count := range []uint32{100, 200, 300} {
			if allowed, _ := l.Allow("val1", 1000, "m1", count); !allowed {
				t.Fatalf("repeat of count %d should be allowed", count)
			}
		}
		// Still rejected after repeats.
		if allowed, _ := l.Allow("val1", 1000, "m1", 500); allowed {
			t.Fatal("new distinct count should stay rejected")
		}
	})

	t.Run("quota is per validator, stage, and model", func(t *testing.T) {
		l := newSnapshotCountLimiter()
		for _, count := range []uint32{1, 2, 3} {
			l.Allow("val1", 1000, "m1", count)
		}
		if allowed, _ := l.Allow("val2", 1000, "m1", 4); !allowed {
			t.Fatal("other validator must have its own quota")
		}
		if allowed, _ := l.Allow("val1", 2000, "m1", 4); !allowed {
			t.Fatal("other stage must have its own quota")
		}
		if allowed, _ := l.Allow("val1", 1000, "m2", 4); !allowed {
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
		if allowed, _ := l.Allow("val1", 1000, "m1", 4); allowed {
			t.Fatal("quota should be exhausted")
		}

		current = current.Add(snapshotLimiterIdleTTL + time.Minute)
		if allowed, _ := l.Allow("val1", 1000, "m1", 4); !allowed {
			t.Fatal("expired entry should reset the quota")
		}
	})
}
