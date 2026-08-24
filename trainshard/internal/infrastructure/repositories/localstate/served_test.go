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
	return store.Served(clock, window)
}

func TestASpentRequestIDSurvivesARestartOfTheDaemon(t *testing.T) {
	// arrange
	dir, clock := t.TempDir(), timex.NewFrozen(now)
	if first, err := openServed(t, dir, clock).First(context.Background(), "gonka1creator/req-1"); err != nil || !first {
		t.Fatalf("got %v %v, want the first request through", first, err)
	}

	// act
	first, err := openServed(t, dir, clock).First(context.Background(), "gonka1creator/req-1")

	// assert
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first {
		t.Fatal("a restart must not hand a caught request a second chance")
	}
}

func TestASpentRequestIDIsForgottenOnceItsSignatureIsStaleAnyway(t *testing.T) {
	// arrange
	dir, clock := t.TempDir(), timex.NewFrozen(now)
	served := openServed(t, dir, clock)
	if _, err := served.First(context.Background(), "gonka1creator/req-1"); err != nil {
		t.Fatalf("first: %v", err)
	}
	clock.Advance(2 * window)

	// act
	first, err := served.First(context.Background(), "gonka1creator/req-1")

	// assert
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if !first {
		t.Fatal("ids are kept only as long as a signature lives, and this one is long dead")
	}
}
