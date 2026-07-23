package router

import (
	"errors"
	"testing"
)

func TestStateEvacuationAndReplacementFollowTransitionTable(t *testing.T) {
	state := mustNewState(t, []string{"versiond-1", "versiond-2"}, "", nil)
	membershipID := membershipOf(t, state, "versiond-2")

	for _, edge := range []struct {
		operationID string
		from        HostState
		to          HostState
		target      HostState
	}{
		{"evacuate-2", HostActive, HostDraining, HostOffline},
		{"evacuate-2", HostDraining, HostStopping, HostOffline},
		{"evacuate-2", HostStopping, HostOffline, HostOffline},
		{"replace-2", HostOffline, HostJoining, HostActive},
		{"replace-2", HostJoining, HostActive, HostActive},
	} {
		result, err := state.Advance(Transition{
			OperationID:  edge.operationID,
			MembershipID: membershipID,
			Host:         "versiond-2",
			From:         edge.from,
			To:           edge.to,
			Target:       edge.target,
		})
		if err != nil {
			t.Fatalf("%s -> %s: %v", edge.from, edge.to, err)
		}
		if !result.Changed {
			t.Fatalf("%s -> %s did not change state", edge.from, edge.to)
		}
	}

	host := state.Hosts[state.hostIndex("versiond-2")]
	if host.State != HostActive || host.MembershipID != membershipID {
		t.Fatalf("replacement host = %#v", host)
	}
	if state.ActiveTransfer != nil {
		t.Fatalf("completed transfer remains active: %#v", state.ActiveTransfer)
	}
}

func TestStateRejectsMissingOrSkippedTransitionHandler(t *testing.T) {
	state := mustNewState(t, []string{"versiond-1", "versiond-2"}, "", nil)

	for _, change := range []Transition{
		{
			OperationID: "skip-drain", Host: "versiond-2",
			From: HostActive, To: HostStopping, Target: HostOffline,
		},
		{
			OperationID: "invent-edge", Host: "versiond-2",
			From: HostActive, To: HostOffline, Target: HostOffline,
		},
	} {
		if _, err := state.Advance(change); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("transition %#v error = %v, want ErrInvalidTransition", change, err)
		}
	}
}

func TestStateTransitionChecksExpectedFromState(t *testing.T) {
	state := mustNewState(t, []string{"versiond-1", "versiond-2"}, "", nil)
	_, err := state.Advance(Transition{
		OperationID: "wrong-from", Host: "versiond-2",
		From: HostOffline, To: HostJoining, Target: HostActive,
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("transition error = %v, want ErrInvalidTransition", err)
	}
}

func TestStateRejectsParametersThatDoNotBelongToTransferTarget(t *testing.T) {
	for _, change := range []Transition{
		{
			OperationID: "bad-address", Host: "versiond-2",
			From: HostActive, To: HostDraining, Target: HostOffline,
			Address: "replacement-2",
		},
		{
			OperationID: "bad-legacy", Host: "versiond-2",
			From: HostOffline, To: HostJoining, Target: HostActive,
			LegacyHost: "versiond-1",
		},
		{
			OperationID: "bad-force", Host: "versiond-2",
			From: HostOffline, To: HostJoining, Target: HostActive,
			Force: true,
		},
	} {
		state := mustNewState(t, []string{"versiond-1", "versiond-2"}, "", nil)
		if change.From == HostOffline {
			moveHostOffline(
				t,
				&state,
				"versiond-2",
				"prepare-offline",
				HostOffline,
				false,
			)
		}
		if _, err := state.Advance(change); err == nil {
			t.Fatalf("transition %#v accepted unrelated parameters", change)
		}
	}
}

func TestStateTransferOwnsAllIntermediateTransitions(t *testing.T) {
	state := mustNewState(
		t,
		[]string{"versiond-1", "versiond-2", "versiond-3"},
		"",
		nil,
	)
	advanceTestState(
		t,
		&state,
		"owner",
		"versiond-2",
		HostActive,
		HostDraining,
		HostOffline,
		false,
	)

	_, err := state.Advance(Transition{
		OperationID: "other", Host: "versiond-2",
		From: HostDraining, To: HostStopping, Target: HostOffline,
	})
	if !errors.Is(err, ErrHostOperation) {
		t.Fatalf("different owner error = %v, want ErrHostOperation", err)
	}
	_, err = state.Advance(Transition{
		OperationID: "other-host", Host: "versiond-3",
		From: HostActive, To: HostDraining, Target: HostOffline,
	})
	if !errors.Is(err, ErrHostOperation) {
		t.Fatalf("concurrent host error = %v, want ErrHostOperation", err)
	}
}

func TestStateTransitionRejectsDifferentMembership(t *testing.T) {
	state := mustNewState(t, []string{"versiond-1", "versiond-2"}, "", nil)
	_, err := state.Advance(Transition{
		OperationID: "evacuate-2", MembershipID: "membership-stale",
		Host: "versiond-2", From: HostActive, To: HostDraining,
		Target: HostOffline,
	})
	if !errors.Is(err, ErrMembershipMismatch) {
		t.Fatalf("membership error = %v, want ErrMembershipMismatch", err)
	}
}

func TestStateTransitionRetryIsIdempotent(t *testing.T) {
	state := mustNewState(t, []string{"versiond-1", "versiond-2"}, "", nil)
	change := Transition{
		OperationID: "evacuate-2", Host: "versiond-2",
		From: HostActive, To: HostDraining, Target: HostOffline,
	}
	first, err := state.Advance(change)
	if err != nil {
		t.Fatal(err)
	}
	generation := state.Generation
	retry, err := state.Advance(change)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed || retry.Changed {
		t.Fatalf("first changed=%v retry changed=%v", first.Changed, retry.Changed)
	}
	if state.Generation != generation {
		t.Fatalf("retry changed generation to %d, want %d", state.Generation, generation)
	}
}

func TestStateCancelReturnsDrainingMembershipToActive(t *testing.T) {
	state := mustNewState(t, []string{"versiond-1", "versiond-2"}, "", nil)
	advanceTestState(
		t,
		&state,
		"evacuate-2",
		"versiond-2",
		HostActive,
		HostDraining,
		HostOffline,
		false,
	)
	result, err := state.Cancel(CancelTransfer{
		OperationID: "evacuate-2",
		Host:        "versiond-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || !result.Canceled {
		t.Fatalf("cancel result = %#v", result)
	}
	if state.Hosts[state.hostIndex("versiond-2")].State != HostActive {
		t.Fatal("canceled host is not active")
	}
	if state.ActiveTransfer != nil {
		t.Fatalf("canceled transfer remains active: %#v", state.ActiveTransfer)
	}
}

func TestStateCancelIsRejectedAfterStoppingBegins(t *testing.T) {
	state := mustNewState(t, []string{"versiond-1", "versiond-2"}, "", nil)
	advanceTestState(
		t,
		&state,
		"evacuate-2",
		"versiond-2",
		HostActive,
		HostDraining,
		HostOffline,
		false,
	)
	advanceTestState(
		t,
		&state,
		"evacuate-2",
		"versiond-2",
		HostDraining,
		HostStopping,
		HostOffline,
		false,
	)
	_, err := state.Cancel(CancelTransfer{
		OperationID: "evacuate-2",
		Host:        "versiond-2",
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("cancel error = %v, want ErrInvalidTransition", err)
	}
}

func TestStateRemoveIsTerminalAndReAddCreatesNewMembership(t *testing.T) {
	state := mustNewState(t, []string{"versiond-1", "versiond-2"}, "", nil)
	oldMembership := membershipOf(t, state, "versiond-2")
	moveHostOffline(t, &state, "versiond-2", "decommission-2", HostRemoved, false)
	result, err := state.Advance(Transition{
		OperationID: "decommission-2", MembershipID: oldMembership,
		Host: "versiond-2", From: HostOffline, To: HostRemoved,
		Target: HostRemoved,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed || state.hostIndex("versiond-2") >= 0 {
		t.Fatalf("remove result=%#v hosts=%#v", result, state.Hosts)
	}
	if _, err := state.Advance(Transition{
		OperationID: "stale-remove", MembershipID: oldMembership,
		Host: "versiond-2", From: HostOffline, To: HostRemoved,
		Target: HostRemoved,
	}); err == nil {
		t.Fatal("removed membership accepted another transition")
	}

	added, err := state.Add(AddMembership{
		OperationID: "add-2",
		Host:        "versiond-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if added.MembershipID == oldMembership {
		t.Fatal("re-added host reused the removed membership id")
	}
}

func TestStateRemoveReassignsLegacyHostAtomically(t *testing.T) {
	state := mustNewState(
		t,
		[]string{"versiond-1", "versiond-2"},
		"versiond-1",
		nil,
	)
	_, err := state.Advance(Transition{
		OperationID: "remove-legacy", Host: "versiond-1",
		From: HostActive, To: HostDraining, Target: HostRemoved,
	})
	if err == nil {
		t.Fatal("legacy host removal without replacement succeeded")
	}
	decommissionHost(
		t,
		&state,
		"remove-legacy",
		"versiond-1",
		"versiond-2",
		false,
	)
	if state.LegacyHost != "versiond-2" || state.hostIndex("versiond-1") >= 0 {
		t.Fatalf("legacy host=%q hosts=%#v", state.LegacyHost, state.Hosts)
	}
}

func TestStateRemoveLegacyNonHAVersionsRequiresForce(t *testing.T) {
	state := mustNewState(
		t,
		[]string{"versiond-1", "versiond-2"},
		"versiond-1",
		[]string{"v1"},
	)
	change := Transition{
		OperationID: "remove-legacy", Host: "versiond-1",
		From: HostActive, To: HostDraining, Target: HostRemoved,
		LegacyHost: "versiond-2",
	}
	if _, err := state.Advance(change); !errors.Is(err, ErrLegacyHost) {
		t.Fatalf("legacy remove error = %v, want ErrLegacyHost", err)
	}
	decommissionHost(
		t,
		&state,
		"remove-legacy",
		"versiond-1",
		"versiond-2",
		true,
	)
}

func TestStateRejectsLastConfiguredHostRemoval(t *testing.T) {
	state := mustNewState(t, []string{"versiond-1"}, "", nil)
	_, err := state.Advance(Transition{
		OperationID: "remove-last", Host: "versiond-1",
		From: HostActive, To: HostDraining, Target: HostRemoved,
		LegacyHost: "versiond-1", Force: true,
	})
	if !errors.Is(err, ErrLastConfiguredHost) {
		t.Fatalf("remove error = %v, want ErrLastConfiguredHost", err)
	}
}

func TestStateRejectsLastActiveAndLegacyHostDrain(t *testing.T) {
	last := mustNewState(t, []string{"versiond-1"}, "", nil)
	generation := last.Generation
	_, err := last.Advance(Transition{
		OperationID: "drain-last", Host: "versiond-1",
		From: HostActive, To: HostDraining, Target: HostOffline,
	})
	if !errors.Is(err, ErrLastActiveHost) {
		t.Fatalf("last active error = %v, want ErrLastActiveHost", err)
	}
	if last.Generation != generation || last.ActiveTransfer != nil ||
		last.Hosts[0].State != HostActive {
		t.Fatalf("rejected handler mutated state: %#v", last)
	}

	legacy := mustNewState(
		t,
		[]string{"versiond-1", "versiond-2"},
		"versiond-1",
		[]string{"v1"},
	)
	_, err = legacy.Advance(Transition{
		OperationID: "drain-legacy", Host: "versiond-1",
		From: HostActive, To: HostDraining, Target: HostOffline,
	})
	if !errors.Is(err, ErrLegacyHost) {
		t.Fatalf("legacy error = %v, want ErrLegacyHost", err)
	}
}

func TestStateJoinCanReplaceAddressAndRejectsDuplicate(t *testing.T) {
	state := mustNewState(t, []string{"versiond-1", "versiond-2"}, "", nil)
	moveHostOffline(t, &state, "versiond-2", "evacuate-2", HostOffline, false)
	_, err := state.Advance(Transition{
		OperationID: "replace-2", Host: "versiond-2",
		From: HostOffline, To: HostJoining, Target: HostActive,
		Address: "replacement-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Hosts[state.hostIndex("versiond-2")].Address; got != "replacement-2" {
		t.Fatalf("replacement address = %q", got)
	}

	other := mustNewState(t, []string{"versiond-1", "versiond-2"}, "", nil)
	moveHostOffline(t, &other, "versiond-2", "evacuate-2", HostOffline, false)
	_, err = other.Advance(Transition{
		OperationID: "replace-2", Host: "versiond-2",
		From: HostOffline, To: HostJoining, Target: HostActive,
		Address: "versiond-1",
	})
	if err == nil {
		t.Fatal("duplicate replacement address was accepted")
	}
}

func mustNewState(
	t *testing.T,
	hosts []string,
	legacyHost string,
	nonHAVersions []string,
) State {
	t.Helper()
	state, err := NewState(hosts, 8080, legacyHost, nonHAVersions)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func membershipOf(t *testing.T, state State, host string) string {
	t.Helper()
	index := state.hostIndex(host)
	if index < 0 {
		t.Fatalf("host %s not found", host)
	}
	return state.Hosts[index].MembershipID
}

func advanceTestState(
	t *testing.T,
	state *State,
	operationID string,
	host string,
	from HostState,
	to HostState,
	target HostState,
	force bool,
) {
	t.Helper()
	if _, err := state.Advance(Transition{
		OperationID: operationID,
		Host:        host,
		From:        from,
		To:          to,
		Target:      target,
		Force:       force,
	}); err != nil {
		t.Fatalf("%s %s -> %s: %v", host, from, to, err)
	}
}

func moveHostOffline(
	t *testing.T,
	state *State,
	host string,
	operationID string,
	target HostState,
	force bool,
) {
	t.Helper()
	for _, edge := range [][2]HostState{
		{HostActive, HostDraining},
		{HostDraining, HostStopping},
		{HostStopping, HostOffline},
	} {
		advanceTestState(
			t,
			state,
			operationID,
			host,
			edge[0],
			edge[1],
			target,
			force,
		)
	}
}

func decommissionHost(
	t *testing.T,
	state *State,
	operationID string,
	host string,
	legacyHost string,
	force bool,
) {
	t.Helper()
	for _, edge := range [][2]HostState{
		{HostActive, HostDraining},
		{HostDraining, HostStopping},
		{HostStopping, HostOffline},
		{HostOffline, HostRemoved},
	} {
		if _, err := state.Advance(Transition{
			OperationID: operationID,
			Host:        host,
			From:        edge[0],
			To:          edge[1],
			Target:      HostRemoved,
			LegacyHost:  legacyHost,
			Force:       force,
		}); err != nil {
			t.Fatalf("%s %s -> %s: %v", host, edge[0], edge[1], err)
		}
	}
}
