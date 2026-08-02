package host

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestStateTableTransitionMatrix(t *testing.T) {
	states := []State{
		StateStarting,
		StateServing,
		StateDraining,
		StateStopping,
		StateForcing,
		StateStopped,
	}
	allowed := map[State]map[State]bool{
		StateStarting: {
			StateStarting: true,
			StateServing:  true,
			StateDraining: true,
			StateForcing:  true,
		},
		StateServing: {
			StateServing:  true,
			StateDraining: true,
			StateForcing:  true,
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
