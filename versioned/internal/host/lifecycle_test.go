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
