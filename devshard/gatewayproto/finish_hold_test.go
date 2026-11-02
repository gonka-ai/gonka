package gatewayproto

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestFinishHoldTransfersReleaseResponsibility(t *testing.T) {
	var released atomic.Int32
	hold := NewFinishHold(func() { released.Add(1) })
	hold.Take()
	hold.ReleaseUntaken()
	if released.Load() != 0 {
		t.Fatal("sender released the receiver's Finish")
	}
	hold.Release()
	hold.Release()
	if released.Load() != 1 {
		t.Fatal("Finish must be released exactly once")
	}
}

func TestFinishHoldUntakenCleanupCannotBeRevived(t *testing.T) {
	var released atomic.Int32
	hold := NewFinishHold(func() { released.Add(1) })
	hold.ReleaseUntaken()
	hold.Take()
	hold.ReleaseUntaken()
	hold.Release()
	if released.Load() != 1 {
		t.Fatal("cleaned-up Finish was released again")
	}
}

func TestFinishHoldCopiesShareOneOwner(t *testing.T) {
	var released atomic.Int32
	proof := struct{ finish *FinishHold }{NewFinishHold(func() { released.Add(1) })}
	copied := proof
	copied.finish.Take()
	proof.finish.ReleaseUntaken()
	if released.Load() != 0 {
		t.Fatal("proof copy lost transferred ownership")
	}
	proof.finish.Release()
	copied.finish.Release()
	if released.Load() != 1 {
		t.Fatal("copies released a Finish twice")
	}
}

func TestFinishHoldConcurrentCleanupReleasesOnce(t *testing.T) {
	var released atomic.Int32
	hold := NewFinishHold(func() { released.Add(1) })
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() { defer workers.Done(); hold.Take(); hold.ReleaseUntaken(); hold.Release() }()
	}
	workers.Wait()
	if released.Load() != 1 {
		t.Fatalf("releases = %d, want 1", released.Load())
	}
	var missing *FinishHold
	missing.Take()
	missing.ReleaseUntaken()
	missing.Release()
}
