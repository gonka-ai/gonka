package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"common/chain"
)

type fakeEpochSource struct{ epoch atomic.Uint64 }

func (f *fakeEpochSource) CurrentEpochID() uint64 { return f.epoch.Load() }

func TestSeedEpochWhenKnown_AppliesFirstSnapshotEpoch(t *testing.T) {
	src := &fakeEpochSource{}
	phase := new(chain.Phase)
	applied := make(chan uint64, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go seedEpochWhenKnown(ctx, src, phase, 5*time.Millisecond, func(e uint64) {
		phase.SetEpoch(e)
		applied <- e
	})

	time.Sleep(20 * time.Millisecond)
	src.epoch.Store(406)
	select {
	case e := <-applied:
		if e != 406 || phase.EpochID() != 406 {
			t.Fatalf("applied %d, phase %d; want 406", e, phase.EpochID())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("epoch from the first snapshot was never applied")
	}
}

func TestSeedEpochWhenKnown_SkipsWhenPhaseAlreadySet(t *testing.T) {
	src := &fakeEpochSource{}
	src.epoch.Store(406)
	phase := new(chain.Phase)
	phase.SetEpoch(407)
	called := false
	seedEpochWhenKnown(context.Background(), src, phase, time.Millisecond, func(uint64) { called = true })
	if called {
		t.Fatal("apply called although OnEpochChange already set the phase")
	}
}

func TestSeedEpochWhenKnown_StopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		seedEpochWhenKnown(ctx, &fakeEpochSource{}, new(chain.Phase), time.Millisecond, func(uint64) { t.Error("apply called") })
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("did not return after cancel")
	}
}
