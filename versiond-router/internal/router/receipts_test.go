package router

import (
	"context"
	"testing"
	"time"
)

func TestLookupOperationReturnsOneDurableCompletion(t *testing.T) {
	controller, _, _ := newTestController(t)
	completedAt := time.Now().UTC()
	if err := controller.persistCompletionReceipt(
		"decommission-versiond-2",
		operationReceipt{
			Action:       "transfer",
			Host:         "versiond-2",
			MembershipID: "membership-versiond-2",
			Target:       HostRemoved,
			Result:       "completed",
			CompletedAt:  completedAt,
		},
	); err != nil {
		t.Fatal(err)
	}

	lookup, err := controller.LookupOperation(
		context.Background(),
		"decommission-versiond-2",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !lookup.Completed || lookup.Completion == nil {
		t.Fatalf("operation lookup = %#v, want completed receipt", lookup)
	}
	if lookup.Completion.Host != "versiond-2" ||
		lookup.Completion.Target != HostRemoved ||
		!lookup.Completion.CompletedAt.Equal(completedAt) {
		t.Fatalf("operation completion = %#v", lookup.Completion)
	}

	missing, err := controller.LookupOperation(
		context.Background(),
		"decommission-versiond-3",
	)
	if err != nil {
		t.Fatal(err)
	}
	if missing.Completed || missing.Completion != nil {
		t.Fatalf("missing operation lookup = %#v", missing)
	}
}

func TestLookupOperationPreservesCanceledTransitionResult(t *testing.T) {
	controller, _, _ := newTestController(t)
	state := newTestState(t)
	if _, err := controller.Bootstrap(
		context.Background(),
		staticState(state),
	); err != nil {
		t.Fatal(err)
	}
	operationID := "cancel-versiond-2"
	membershipID := membershipOf(t, state, "versiond-2")
	if _, err := controller.Transition(context.Background(), Transition{
		OperationID:  operationID,
		MembershipID: membershipID,
		Host:         "versiond-2",
		From:         HostActive,
		To:           HostDraining,
		Target:       HostOffline,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Cancel(context.Background(), CancelTransfer{
		OperationID:  operationID,
		MembershipID: membershipID,
		Host:         "versiond-2",
	}); err != nil {
		t.Fatal(err)
	}

	lookup, err := controller.LookupOperation(
		context.Background(),
		operationID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !lookup.Completed || lookup.Completion == nil {
		t.Fatalf("operation lookup = %#v, want canceled receipt", lookup)
	}
	if lookup.Completion.Action != "cancel" ||
		lookup.Completion.Target != HostActive ||
		lookup.Completion.Result != "canceled" {
		t.Fatalf("cancellation receipt = %#v", lookup.Completion)
	}
}
