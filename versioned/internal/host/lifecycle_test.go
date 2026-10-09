package host

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestControllerTransitions(t *testing.T) {
	c := NewController()
	for _, state := range []State{StateServing, StateDraining, StateStopping, StateStopped} {
		if err := c.Transition(state); err != nil {
			t.Fatalf("transition to %s: %v", state, err)
		}
	}
	if err := c.Transition(StateServing); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("stopped -> serving error = %v, want invalid transition", err)
	}
}

func TestBeginDrainChoosesTheCurrentLifecycleEdge(t *testing.T) {
	t.Run("starting host skips announcement", func(t *testing.T) {
		c := NewController()
		announcing, err := c.BeginDrain()
		if err != nil {
			t.Fatal(err)
		}
		if announcing {
			t.Fatal("starting host entered announcement")
		}
		if got := c.Snapshot().State; got != StateDraining {
			t.Fatalf("host state = %s, want draining", got)
		}
	})

	t.Run("serving host announces first", func(t *testing.T) {
		c := NewController()
		if err := c.Transition(StateServing); err != nil {
			t.Fatal(err)
		}
		announcing, err := c.BeginDrain()
		if err != nil {
			t.Fatal(err)
		}
		if !announcing {
			t.Fatal("serving host skipped announcement")
		}
		snapshot := c.Snapshot()
		if snapshot.State != StateAnnouncing || snapshot.Ready || !snapshot.Accepting {
			t.Fatalf("announcement snapshot = %+v", snapshot)
		}
	})
}

func TestPromoteAndBeginDrainAreAtomic(t *testing.T) {
	const iterations = 128
	for iteration := 0; iteration < iterations; iteration++ {
		c := NewController()
		start := make(chan struct{})
		promoted := make(chan bool, 1)
		announcement := make(chan bool, 1)
		drainErr := make(chan error, 1)

		go func() {
			<-start
			promoted <- c.Promote()
		}()
		go func() {
			<-start
			announcing, err := c.BeginDrain()
			announcement <- announcing
			drainErr <- err
		}()
		close(start)

		if err := <-drainErr; err != nil {
			t.Fatalf("iteration %d: begin drain: %v", iteration, err)
		}
		promotionWon := <-promoted
		announcing := <-announcement
		state := c.Snapshot().State
		switch {
		case promotionWon && announcing && state == StateAnnouncing:
		case !promotionWon && !announcing && state == StateDraining:
		default:
			t.Fatalf(
				"iteration %d: promoted=%t announcing=%t state=%s",
				iteration,
				promotionWon,
				announcing,
				state,
			)
		}
	}
}

func TestControllerConcurrentAdmissionAndDrain(t *testing.T) {
	const (
		iterations = 64
		workers    = 32
	)
	for iteration := 0; iteration < iterations; iteration++ {
		c := NewController()
		if err := c.Transition(StateServing); err != nil {
			t.Fatal(err)
		}
		handler := c.Admission(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		start := make(chan struct{})
		statuses := make([]int, workers)
		var wg sync.WaitGroup
		for worker := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				response := httptest.NewRecorder()
				handler.ServeHTTP(
					response,
					httptest.NewRequest(http.MethodGet, "/v1", nil),
				)
				statuses[worker] = response.Code
			}()
		}
		drainDone := make(chan error, 1)
		go func() {
			<-start
			announcing, err := c.BeginDrain()
			if err == nil && announcing {
				err = c.Transition(StateDraining)
			}
			drainDone <- err
		}()

		close(start)
		wg.Wait()
		if err := <-drainDone; err != nil {
			t.Fatal(err)
		}
		for worker, status := range statuses {
			if status != http.StatusNoContent && status != http.StatusServiceUnavailable {
				t.Fatalf("iteration %d worker %d status = %d", iteration, worker, status)
			}
		}

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1", nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("iteration %d post-drain status = %d, want 503", iteration, response.Code)
		}
		snapshot := c.Snapshot()
		if snapshot.State != StateDraining || snapshot.Inflight != 0 || !snapshot.Idle {
			t.Fatalf("iteration %d final snapshot = %+v", iteration, snapshot)
		}
	}
}

func TestStateTableTransitionMatrix(t *testing.T) {
	states := []State{
		StateStarting,
		StateServing,
		StateAnnouncing,
		StateDraining,
		StateStopping,
		StateForcing,
		StateStopped,
	}
	allowed := map[State]map[State]bool{
		// starting -> announcing is deliberately absent: announcing accepts,
		// and a host that never served must not open admission while shutting
		// down.
		StateStarting: {
			StateStarting: true,
			StateServing:  true,
			StateDraining: true,
			StateForcing:  true,
		},
		StateServing: {
			StateServing:    true,
			StateAnnouncing: true,
			StateDraining:   true,
			StateForcing:    true,
		},
		StateAnnouncing: {
			StateAnnouncing: true,
			StateDraining:   true,
			StateForcing:    true,
		},
		StateDraining: {
			StateDraining: true,
			StateStopping: true,
			StateForcing:  true,
		},
		StateStopping: {
			StateStopping: true,
			StateForcing:  true,
			StateStopped:  true,
		},
		StateForcing: {
			StateForcing: true,
			StateStopped: true,
		},
		StateStopped: {
			StateStopped: true,
		},
	}

	for _, from := range states {
		for _, to := range states {
			if got, want := validTransition(from, to), allowed[from][to]; got != want {
				t.Errorf("validTransition(%s, %s) = %t, want %t", from, to, got, want)
			}
		}
	}
	if validTransition("unknown", "unknown") {
		t.Fatal("unknown host state accepted a self-transition")
	}
}

func TestStateTableSeparatesAdmissionFromReadiness(t *testing.T) {
	states := []State{
		StateStarting,
		StateServing,
		StateAnnouncing,
		StateDraining,
		StateStopping,
		StateForcing,
		StateStopped,
	}
	if len(stateTable) != len(states) {
		t.Fatalf("host state table has %d states, want %d", len(stateTable), len(states))
	}

	accepting := map[State]bool{}
	ready := map[State]bool{}
	for state, spec := range stateTable {
		if spec.accepting {
			accepting[state] = true
		}
		if spec.ready {
			ready[state] = true
		}
		if spec.ready && !spec.accepting {
			t.Errorf("%s advertises ready without accepting work", state)
		}
		for _, target := range spec.targets {
			if _, ok := stateTable[target]; !ok {
				t.Errorf("%s targets unknown host state %s", state, target)
			}
		}
	}

	// announcing is the drain window: still serving, already unready.
	if !accepting[StateServing] || !accepting[StateAnnouncing] || len(accepting) != 2 {
		t.Fatalf("accepting states = %v, want serving and announcing", accepting)
	}
	if !ready[StateServing] || len(ready) != 1 {
		t.Fatalf("ready states = %v, want serving only", ready)
	}
	if len(stateTable[StateStopped].targets) != 0 {
		t.Fatal("stopped host state has outgoing targets")
	}
}

func TestControllerForceTransition(t *testing.T) {
	for _, state := range []State{StateStarting, StateServing, StateDraining, StateStopping} {
		t.Run(string(state), func(t *testing.T) {
			c := NewController()
			if state != StateStarting {
				if err := c.Transition(StateServing); err != nil {
					t.Fatal(err)
				}
			}
			if state == StateDraining || state == StateStopping {
				if err := c.Transition(StateDraining); err != nil {
					t.Fatal(err)
				}
			}
			if state == StateStopping {
				if err := c.Transition(StateStopping); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.Transition(StateForcing); err != nil {
				t.Fatalf("%s -> forcing: %v", state, err)
			}
			if err := c.Transition(StateStopped); err != nil {
				t.Fatalf("forcing -> stopped: %v", err)
			}
		})
	}
}

func TestAdmissionHoldsLeaseForCompleteRequest(t *testing.T) {
	c := NewController()
	if err := c.Transition(StateServing); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	handler := c.Admission(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	server := httptest.NewServer(handler)
	defer server.Close()

	done := make(chan error, 1)
	go func() {
		resp, err := http.Get(server.URL)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			err = resp.Body.Close()
		}
		done <- err
	}()
	<-started

	if got := c.Snapshot().Inflight; got != 1 {
		t.Fatalf("inflight = %d, want 1", got)
	}
	if err := c.Transition(StateDraining); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("new request status = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.WaitIdle(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitIdle error = %v, want deadline exceeded", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := c.WaitIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
}
