package localstate_test

import (
	"context"
	"testing"
	"time"

	"trainshard/internal/infrastructure/repositories/localstate"
	"trainshard/internal/utils/signedhttp"
	"trainshard/internal/utils/timex"
)

const window = time.Minute

func openServed(t *testing.T, dir string, clock *timex.Frozen) signedhttp.Served {
	t.Helper()

	store, err := localstate.New(dir)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	return store.Served(clock)
}

func TestASpentRequestIDSurvivesARestartOfTheDaemon(t *testing.T) {
	// arrange
	dir, clock := t.TempDir(), timex.NewFrozen(now)
	if first, err := openServed(t, dir, clock).First(context.Background(), "gonka1creator/req-1", now.Add(window)); err != nil || !first {
		t.Fatalf("got %v %v, want the first request through", first, err)
	}

	// act
	first, err := openServed(t, dir, clock).First(context.Background(), "gonka1creator/req-1", now.Add(window))

	// assert
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first {
		t.Fatal("a restart must not hand a caught request a second chance")
	}
}

// The id outlives the request that spent it by whatever its own signature had left, not by a span
// counted from the moment it arrived
func TestASpentRequestIDIsHeldForAsLongAsItsSignatureStillPasses(t *testing.T) {
	// arrange
	dir, clock := t.TempDir(), timex.NewFrozen(now)
	served := openServed(t, dir, clock)
	staleAfter := now.Add(2 * window)
	if _, err := served.First(context.Background(), "gonka1creator/req-1", staleAfter); err != nil {
		t.Fatalf("first: %v", err)
	}
	clock.Advance(2 * window)

	// act
	first, err := served.First(context.Background(), "gonka1creator/req-1", staleAfter)

	// assert
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first {
		t.Fatal("an id must be held while a signature carrying it can still pass")
	}
}

func TestASpentRequestIDIsForgottenOnceItsSignatureIsStaleAnyway(t *testing.T) {
	// arrange
	dir, clock := t.TempDir(), timex.NewFrozen(now)
	served := openServed(t, dir, clock)
	if _, err := served.First(context.Background(), "gonka1creator/req-1", now.Add(window)); err != nil {
		t.Fatalf("first: %v", err)
	}
	clock.Advance(2 * window)

	// act
	first, err := served.First(context.Background(), "gonka1creator/req-1", clock.Now().Add(window))

	// assert
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if !first {
		t.Fatal("ids are kept only as long as a signature lives, and this one is long dead")
	}
}
