package config

import (
	"sync"
	"testing"
)

// Test flow:
//  1. Build a `Holder` from an initial config.
//  2. Assert `Load` returns the exact pointer given to `NewHolder`.
func TestHolderLoadReturnsInitialSnapshot(t *testing.T) {
	initial := Defaults()
	holder := NewHolder(&initial)
	if holder.Load() != &initial {
		t.Fatal("Load() must return the exact pointer given to NewHolder")
	}
}

// Test flow:
//  1. Build a `Holder` and subscribe a callback that records every snapshot it sees.
//  2. Swap in a new config.
//  3. Assert `Load` returns the new config and the subscriber was notified exactly once with the swapped snapshot.
func TestHolderSwapNotifiesSubscribersWithNewSnapshot(t *testing.T) {
	initial := Defaults()
	holder := NewHolder(&initial)

	var mutex sync.Mutex
	var seen []*Config
	cancel := holder.Subscribe(func(next *Config) {
		mutex.Lock()
		defer mutex.Unlock()
		seen = append(seen, next)
	})
	defer cancel()

	next := Defaults()
	next.Server.Port = 9999
	holder.Swap(&next)

	if holder.Load().Server.Port != 9999 {
		t.Fatalf("Load().Server.Port = %d, want 9999 after Swap", holder.Load().Server.Port)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if len(seen) != 1 || seen[0] != &next {
		t.Fatalf("subscriber saw %v, want exactly the swapped snapshot once", seen)
	}
}

// Test flow:
//  1. Subscribe a callback, then cancel it immediately.
//  2. Swap in a new config.
//  3. Assert the cancelled subscriber was never notified.
func TestHolderCancelledSubscriberIsNotNotified(t *testing.T) {
	initial := Defaults()
	holder := NewHolder(&initial)

	notified := false
	cancel := holder.Subscribe(func(*Config) { notified = true })
	cancel()

	next := Defaults()
	holder.Swap(&next)
	if notified {
		t.Fatal("cancelled subscriber must not be notified")
	}
}

// Test flow:
//  1. Build a `Holder`.
//  2. Run four pairs of goroutines concurrently: one swapping in a fresh config 500 times, one loading the port 500 times.
//  3. Assert the run completes without a data race (verified under `go test -race`).
func TestHolderConcurrentLoadAndSwapIsRaceFree(t *testing.T) {
	initial := Defaults()
	holder := NewHolder(&initial)

	var waitGroup sync.WaitGroup
	for range 4 {
		waitGroup.Add(2)
		go func() {
			defer waitGroup.Done()
			for range 500 {
				snapshot := Defaults()
				holder.Swap(&snapshot)
			}
		}()
		go func() {
			defer waitGroup.Done()
			for range 500 {
				_ = holder.Load().Server.Port
			}
		}()
	}
	waitGroup.Wait()
}
