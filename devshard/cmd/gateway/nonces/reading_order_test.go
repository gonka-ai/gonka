package nonces

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"devshard/cmd/gateway/registry"
)

const pausedCreationEpoch = 7

type unpublishableSource struct {
	session     *scriptedSession
	unpublished atomic.Bool
}

func (s *unpublishableSource) Snapshot() []registry.EscrowState {
	if s.unpublished.Load() {
		return nil
	}
	return []registry.EscrowState{{ID: tokenEscrowID, Model: "llama"}}
}

func (s *unpublishableSource) RoutableSession(string) (registry.EscrowSession, bool) {
	return s.session, !s.unpublished.Load()
}

func pauseTheFirstEpochLookup(ledger *Recorder, resume <-chan struct{}) <-chan struct{} {
	paused := make(chan struct{})
	pause := sync.OnceFunc(func() {
		close(paused)
		<-resume
	})
	ledger.SetCreationEpoch(func(context.Context, string) (uint64, bool) {
		pause()
		return pausedCreationEpoch, true
	})
	return paused
}

// Test flow:
//  1. Sweep two started nonces into a stored ledger, then finish nonce 1 live.
//  2. Start a second sweep and hold it inside its creation-epoch lookup.
//  3. While it is held, seal nonce 2 and retire the escrow the way the registry does: unpublish it, then hand its session to `EscrowRetiring`.
//  4. Let the sweep finish, close the ledger, and read the store.
//  5. Assert both nonces are counted once with no estimated error, and the escrow stays retired.
func TestASweepCannotUndoAReadingTakenWhileItRan(t *testing.T) {
	path := DatabasePath(t.TempDir())
	session := twoStartedNonces()
	source := &unpublishableSource{session: session}
	ledger := storedRecorder(t, path, session.state.Group)
	ledger.sweep(t.Context(), source, nil)
	session.finishLive(1, restartFirstInputTokens)

	resume := make(chan struct{})
	paused := pauseTheFirstEpochLookup(ledger, resume)
	swept := make(chan struct{})
	go func() {
		defer close(swept)
		ledger.sweep(t.Context(), source, nil)
	}()
	<-paused
	session.finishAndSeal(2, restartSecondInputTokens)
	source.unpublished.Store(true)
	ledger.EscrowRetiring(tokenEscrowID, session)
	close(resume)
	<-swept
	closeService(t, ledger)

	restored := storedLedger(t, path)
	assertBothNoncesCounted(t, restored, "after a sweep overlapped the retirement")
	if !restored.retired {
		t.Fatalf("the escrow was saved serving after it retired")
	}
}

func holdTheFirstSnapshot(session *scriptedSession, release <-chan struct{}) <-chan struct{} {
	held := make(chan struct{})
	var taken atomic.Bool
	session.onSnapshot = func() {
		if taken.CompareAndSwap(false, true) {
			close(held)
			<-release
		}
	}
	return held
}

// Test flow:
//  1. Inside a synctest bubble, sweep two started nonces into a stored ledger, then finish nonce 1 live.
//  2. Start a second sweep and hold it inside the session snapshot it reads from.
//  3. While it is held, seal nonce 2, unpublish the escrow and call `EscrowRetiring` from another goroutine, then wait until every goroutine is blocked.
//  4. Assert the retirement has not returned: its reading waits for the sweep's.
//  5. Release the sweep, wait for both readings, close the ledger and read the store.
//  6. Assert both nonces are counted once with no estimated error, and the escrow stays retired.
func TestARetirementReadingWaitsForTheSweepsReadingInProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := DatabasePath(t.TempDir())
		session := twoStartedNonces()
		source := &unpublishableSource{session: session}
		ledger := storedRecorder(t, path, session.state.Group)
		ledger.sweep(t.Context(), source, nil)
		session.finishLive(1, restartFirstInputTokens)

		release := make(chan struct{})
		held := holdTheFirstSnapshot(session, release)
		swept := make(chan struct{})
		go func() {
			defer close(swept)
			ledger.sweep(t.Context(), source, nil)
		}()
		<-held
		session.finishAndSeal(2, restartSecondInputTokens)
		source.unpublished.Store(true)
		retired := make(chan struct{})
		go func() {
			defer close(retired)
			ledger.EscrowRetiring(tokenEscrowID, session)
		}()
		synctest.Wait()

		select {
		case <-retired:
			t.Errorf("the retirement took its reading while the sweep held a snapshot")
		default:
		}
		close(release)
		<-swept
		<-retired
		closeService(t, ledger)

		restored := storedLedger(t, path)
		assertBothNoncesCounted(t, restored, "after a retirement waited for the sweep")
		if !restored.retired {
			t.Fatalf("the escrow was saved serving after it retired")
		}
	})
}

func holdTheFirstEpochLookupUntilCancelled(ledger *Recorder) (paused, cancelled <-chan struct{}) {
	pausedSignal, cancelledSignal := make(chan struct{}), make(chan struct{})
	markPaused := sync.OnceFunc(func() { close(pausedSignal) })
	markCancelled := sync.OnceFunc(func() { close(cancelledSignal) })
	ledger.SetCreationEpoch(func(ctx context.Context, _ string) (uint64, bool) {
		markPaused()
		<-ctx.Done()
		markCancelled()
		return 0, false
	})
	return pausedSignal, cancelledSignal
}

func stopReportingCancellation(ledger *Recorder, cancelled <-chan struct{}) <-chan bool {
	stopped := make(chan bool, 1)
	go func() {
		ledger.Stop()
		select {
		case <-cancelled:
			stopped <- true
		default:
			stopped <- false
		}
	}()
	return stopped
}

// Test flow:
//  1. Inside a synctest bubble, sweep two started nonces into a stored ledger, finish nonce 1 live, and start the sweep loop, holding its next sweep inside a creation-epoch lookup that ends only when its context does.
//  2. Shut down in the gateway's order: `Stop` the sweep from another goroutine, wait until every goroutine is blocked, and assert `Stop` returned and had cancelled the held lookup by then; then seal nonce 2, empty the live set and hand the session to `EscrowClosing`, as `Registry.Close` does, and wake the sweep.
//  3. Close the ledger and read the store.
//  4. Assert both nonces are counted once with no estimated error and the escrow was not retired.
func TestAShutdownRetiresNoEscrowInTheLedger(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := DatabasePath(t.TempDir())
		session := twoStartedNonces()
		source := &unpublishableSource{session: session}
		ledger := storedRecorder(t, path, session.state.Group)
		ledger.sweep(t.Context(), source, nil)
		session.finishLive(1, restartFirstInputTokens)
		paused, cancelled := holdTheFirstEpochLookupUntilCancelled(ledger)
		ledger.StartSweeping(t.Context(), source, nil)
		<-paused

		stopped := stopReportingCancellation(ledger, cancelled)
		synctest.Wait()
		select {
		case cancelledFirst := <-stopped:
			if !cancelledFirst {
				t.Fatalf("Stop returned while the sweep's epoch lookup was still running")
			}
		default:
			t.Fatalf("Stop is still waiting for a sweep it never cancelled")
		}
		session.finishAndSeal(2, restartSecondInputTokens)
		source.unpublished.Store(true)
		ledger.EscrowClosing(tokenEscrowID, session)
		ledger.EscrowPublished(tokenEscrowID, "llama")
		closeService(t, ledger)

		restored := storedLedger(t, path)
		assertBothNoncesCounted(t, restored, "after a shutdown")
		if restored.retired {
			t.Fatalf("the escrow was saved retired by a shutdown that only closed it")
		}
	})
}

// Test flow:
//  1. Sweep two started nonces into a ledger, record a won race on nonce 2, then drop it from the live set with no record kept at seal.
//  2. Sweep again, counting the session's sealed-record lookups.
//  3. Assert nonce 2 was looked up once: the fold and the finish check share one lookup per reading.
func TestAReadingLooksUpEachSealedNonceOnce(t *testing.T) {
	session := twoStartedNonces()
	ledger := storedRecorder(t, DatabasePath(t.TempDir()), session.state.Group)
	defer closeService(t, ledger)
	ledger.sweep(t.Context(), scriptedSource{session: session}, nil)
	recordWonRace(t, ledger, 2)
	delete(session.state.Inferences, 2)
	clear(session.lookups)

	ledger.sweep(t.Context(), scriptedSource{session: session}, nil)

	if lookups := session.lookups[2]; lookups != 1 {
		t.Fatalf("nonce 2 was looked up %d times in one reading, want 1", lookups)
	}
}
