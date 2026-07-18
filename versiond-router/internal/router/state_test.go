package router

import (
	"errors"
	"testing"
)

func TestStateEvacuationAndReplacementTransitions(t *testing.T) {
	state, err := NewState([]string{"versiond-1", "versiond-2"}, 8080, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		action Action
		want   HostState
	}{
		{ActionDrain, HostDraining},
		{ActionOffline, HostOffline},
		{ActionJoin, HostJoining},
		{ActionActivate, HostActive},
	}
	for i, step := range steps {
		operationID := "evacuate-versiond-2"
		if i >= 2 {
			operationID = "replace-versiond-2"
		}
		changed, _, to, err := state.Apply(Transition{
			Action: step.action, Host: "versiond-2", OperationID: operationID,
		})
		if err != nil {
			t.Fatalf("%s: %v", step.action, err)
		}
		if !changed || to != step.want {
			t.Fatalf("%s changed=%v to=%s, want true/%s", step.action, changed, to, step.want)
		}
	}
}

func TestStateRejectsLastActiveHostDrain(t *testing.T) {
	state, err := NewState([]string{"versiond-1"}, 8080, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = state.Apply(Transition{
		Action: ActionDrain, Host: "versiond-1", OperationID: "drain-1",
	})
	if !errors.Is(err, ErrLastActiveHost) {
		t.Fatalf("drain error = %v, want ErrLastActiveHost", err)
	}
}

func TestStateRejectsLegacyHostWithNonHAVersions(t *testing.T) {
	state, err := NewState(
		[]string{"versiond-1", "versiond-2"},
		8080,
		"versiond-1",
		[]string{"v1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = state.Apply(Transition{
		Action: ActionDrain, Host: "versiond-1", OperationID: "drain-legacy",
	})
	if !errors.Is(err, ErrLegacyHost) {
		t.Fatalf("drain error = %v, want ErrLegacyHost", err)
	}
}

func TestStateRejectsConcurrentHostOperations(t *testing.T) {
	state, err := NewState(
		[]string{"versiond-1", "versiond-2", "versiond-3"},
		8080,
		"",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := state.Apply(Transition{
		Action: ActionDrain, Host: "versiond-2", OperationID: "drain-2",
	}); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = state.Apply(Transition{
		Action: ActionDrain, Host: "versiond-3", OperationID: "drain-3",
	})
	if !errors.Is(err, ErrHostOperation) {
		t.Fatalf("concurrent drain error = %v, want ErrHostOperation", err)
	}
}

func TestStateDrainIsIdempotent(t *testing.T) {
	state, err := NewState([]string{"versiond-1", "versiond-2"}, 8080, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := state.Apply(Transition{
		Action: ActionDrain, Host: "versiond-2", OperationID: "first",
	}); err != nil {
		t.Fatal(err)
	}
	generation := state.Generation
	changed, _, _, err := state.Apply(Transition{
		Action: ActionDrain, Host: "versiond-2", OperationID: "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("idempotent drain should not change state")
	}
	if state.Generation != generation {
		t.Fatalf("generation = %d, want %d", state.Generation, generation)
	}
}

func TestStateRejectsDifferentOperationForDrainingHost(t *testing.T) {
	state, err := NewState([]string{"versiond-1", "versiond-2"}, 8080, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := state.Apply(Transition{
		Action: ActionDrain, Host: "versiond-2", OperationID: "owner",
	}); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = state.Apply(Transition{
		Action: ActionOffline, Host: "versiond-2", OperationID: "other",
	})
	if !errors.Is(err, ErrOperationOwner) {
		t.Fatalf("operation owner error = %v, want ErrOperationOwner", err)
	}
}

func TestStateJoinCanReplaceHostAddress(t *testing.T) {
	state, err := NewState([]string{"versiond-1", "versiond-2"}, 8080, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []Action{ActionDrain, ActionOffline} {
		if _, _, _, err := state.Apply(Transition{
			Action: action, Host: "versiond-2", OperationID: "evacuate",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := state.Apply(Transition{
		Action:      ActionJoin,
		Host:        "versiond-2",
		Address:     "replacement-2",
		OperationID: "join-replacement",
	}); err != nil {
		t.Fatal(err)
	}
	if got := state.Hosts[state.hostIndex("versiond-2")].Address; got != "replacement-2" {
		t.Fatalf("replacement address = %q, want replacement-2", got)
	}
}

func TestStateRejectsDuplicateReplacementAddress(t *testing.T) {
	state, err := NewState([]string{"versiond-1", "versiond-2"}, 8080, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []Action{ActionDrain, ActionOffline} {
		if _, _, _, err := state.Apply(Transition{
			Action: action, Host: "versiond-2", OperationID: "evacuate",
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, _, _, err = state.Apply(Transition{
		Action:      ActionJoin,
		Host:        "versiond-2",
		Address:     "versiond-1",
		OperationID: "join-duplicate",
	})
	if err == nil {
		t.Fatal("duplicate replacement address was accepted")
	}
}
