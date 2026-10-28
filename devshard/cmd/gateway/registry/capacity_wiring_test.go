// This is the one black-box test in the package: it reaches for limits and scheduler, which the
// registry itself must never import, to pin the wiring an in-package test cannot see. Missing that
// wiring makes the gateway boot green and serve nothing. See routing.md,
// "Membership: what the capacity model is told".
package registry_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"common/completionapi"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/scheduler"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/user"
)

const (
	wiredEscrowID = "11"
	wiredModel    = "qwen"
)

type openLimiter struct{}

func (openLimiter) Admits(string, string) limits.Admission { return limits.AdmissionOpen }

func (openLimiter) Acquire(string, string, limits.TokenCost) (func(), limits.Admission) {
	return func() {}, limits.AdmissionOpen
}

func (openLimiter) Overdraft(string, string, limits.TokenCost) (func(), limits.Admission) {
	return func() {}, limits.AdmissionOpen
}

type capablePerf struct{}

func (capablePerf) CannotServe(string, string, bool, uint64) (string, bool) { return "", false }
func (capablePerf) Ejected(string, string) bool                             { return false }

type fixedSnapshots struct{ snapshot chain.PhaseSnapshot }

func (f fixedSnapshots) Snapshot() chain.PhaseSnapshot { return f.snapshot }

// liveSessionFactory builds a real *user.Session over in-memory storage with nil host clients.
func liveSessionFactory(t *testing.T) (registry.SessionFactory, []string) {
	t.Helper()
	signers := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	group := testutil.MakeGroup(signers)
	sessionConfig := testutil.DefaultConfig(len(group))
	creator := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()
	store := testutil.MustMemoryStore(t, wiredEscrowID, creator.Address(), sessionConfig, group, 1_000_000)

	machine, err := state.NewStateMachine(wiredEscrowID, sessionConfig, group, 1_000_000, creator.Address(), verifier, store)
	if err != nil {
		t.Fatalf("NewStateMachine = %v, want nil", err)
	}
	session, err := user.NewSession(machine, creator, wiredEscrowID, group, make([]user.HostClient, len(group)), verifier, user.WithStorage(store))
	if err != nil {
		t.Fatalf("NewSession = %v, want nil", err)
	}

	participants := []string{signers[0].Address(), signers[1].Address()}
	return func(context.Context, string) (registry.EscrowSession, error) {
		return registry.NewSessionHandle(session, machine), nil
	}, participants
}

// Test flow:
//  1. Build a live session factory and capacity limiter seeded with two participants' weights (40 and 60), and add one escrow to a registry wired to that capacity as membership.
//  2. Assert the escrow's weight is 100: the sum of its two hosts' current weights, since each holds one of the two slots and serves no other escrow.
//  3. Wire a scheduler around the registry, the capacity limiter, an open admission limiter, and a perf model that reports every host capable.
//  4. Ask the scheduler to pick a host for the wired model.
//  5. Assert the pick succeeds, names the wired escrow, and carries a committed nonce.
func TestPushedMembershipIsWhatMakesTheGatewayRouteAtAll(t *testing.T) {
	t.Parallel()

	sessions, participants := liveSessionFactory(t)
	weights := map[string]float64{participants[0]: 40, participants[1]: 60}
	capacity := limits.NewCapacity(nil)
	capacity.Update(chain.PhaseSnapshot{CurrentWeights: weights, FullWeights: weights})

	escrows := registry.New(registry.Deps{ServingSessions: sessions, Membership: capacity, Now: time.Now})
	t.Cleanup(func() { _ = escrows.Close() })
	if err := escrows.Add(context.Background(), wiredEscrowID, wiredModel); err != nil {
		t.Fatalf("Add = %v, want nil", err)
	}

	if got, want := capacity.EscrowWeight(wiredEscrowID, wiredModel), 100.0; got != want {
		t.Fatalf("EscrowWeight = %v, want %v", got, want)
	}

	settings := config.Defaults()
	router, wiringErr := scheduler.NewScheduler(scheduler.Deps{
		Escrows:   escrows,
		Capacity:  capacity,
		Limiter:   openLimiter{},
		Perf:      capablePerf{},
		Snapshots: fixedSnapshots{},
		Config:    config.NewHolder(&settings),
	})
	if wiringErr != nil {
		t.Fatalf("NewScheduler() = %v, want a wired router", wiringErr)
	}
	t.Cleanup(router.Stop)

	assignment, err := router.Pick(context.Background(), scheduler.RequestProfile{
		Model:  wiredModel,
		Params: user.InferenceParams{Model: wiredModel, Prompt: []byte(`{"messages":[]}`), InputLength: 15, MaxTokens: completionapi.MinTokensFloor},
	})
	if err != nil {
		t.Fatalf("Pick = %v, want an assignment; a zero escrow weight scores every candidate +Inf", err)
	}
	if assignment.Escrow != wiredEscrowID {
		t.Errorf("assigned escrow = %q, want %q", assignment.Escrow, wiredEscrowID)
	}
	if assignment.Nonce == nil || assignment.Nonce.Nonce() == 0 {
		t.Errorf("assignment carries no committed nonce: %+v", assignment.Nonce)
	}
}

// Test flow:
//  1. Build a live session factory and capacity limiter seeded with two participants' weights, and add one escrow to a registry built with no membership wired in (nil, matching the gateway before this package existed).
//  2. Assert the escrow's weight stays 0 without a membership push, even though the escrow is published and looks healthy among the candidates.
//  3. Wire a scheduler around the registry and capacity limiter.
//  4. Ask the scheduler to pick a host for the wired model.
//  5. Assert the pick fails with `scheduler.ErrNoEscrowCapacity`: the gateway boots green and serves nothing.
func TestWithoutTheMembershipPushEveryEscrowScoresUnusable(t *testing.T) {
	t.Parallel()

	sessions, participants := liveSessionFactory(t)
	weights := map[string]float64{participants[0]: 40, participants[1]: 60}
	capacity := limits.NewCapacity(nil)
	capacity.Update(chain.PhaseSnapshot{CurrentWeights: weights, FullWeights: weights})

	escrows := registry.New(registry.Deps{ServingSessions: sessions, Now: time.Now})
	t.Cleanup(func() { _ = escrows.Close() })
	if err := escrows.Add(context.Background(), wiredEscrowID, wiredModel); err != nil {
		t.Fatalf("Add = %v, want nil", err)
	}

	if got := capacity.EscrowWeight(wiredEscrowID, wiredModel); got != 0 {
		t.Fatalf("EscrowWeight without a membership push = %v, want 0", got)
	}
	if got := len(escrows.Candidates(wiredModel)); got != 1 {
		t.Fatalf("len(Candidates) = %d, want 1: the escrow is published and looks healthy", got)
	}

	settings := config.Defaults()
	router, wiringErr := scheduler.NewScheduler(scheduler.Deps{
		Escrows:   escrows,
		Capacity:  capacity,
		Limiter:   openLimiter{},
		Perf:      capablePerf{},
		Snapshots: fixedSnapshots{},
		Config:    config.NewHolder(&settings),
	})
	if wiringErr != nil {
		t.Fatalf("NewScheduler() = %v, want a wired router", wiringErr)
	}
	t.Cleanup(router.Stop)

	_, err := router.Pick(context.Background(), scheduler.RequestProfile{Model: wiredModel})

	if !errors.Is(err, scheduler.ErrNoEscrowCapacity) {
		t.Fatalf("Pick = %v, want ErrNoEscrowCapacity: the gateway boots green and serves nothing", err)
	}
}

// Test flow:
//  1. Build a capacity limiter seeded with a shared participant's weight of 90 and an exclusive participant's weight of 10.
//  2. Set escrow "1"'s membership to half the shared participant plus the whole exclusive participant, and escrow "2"'s membership to the other half of the shared participant.
//  3. Assert escrow "1"'s weight is 55 and escrow "2"'s weight is 45: the shared host's weight splits between the two escrows rather than counting twice.
//  4. Re-set escrow "1"'s membership to the full, unshared weight of both participants.
//  5. Assert escrow "1"'s weight becomes 100: raw slot counts would hand the shared host's full weight to both escrows.
func TestMembershipSharesFeedTheWeightSplitOfASharedParticipant(t *testing.T) {
	t.Parallel()
	shared := testutil.MustGenerateKey(t)
	exclusive := testutil.MustGenerateKey(t)
	capacity := limits.NewCapacity(nil)
	weights := map[string]float64{shared.Address(): 90, exclusive.Address(): 10}
	capacity.Update(chain.PhaseSnapshot{CurrentWeights: weights, FullWeights: weights})

	capacity.SetEscrowMembership("1", map[string]float64{shared.Address(): 0.5, exclusive.Address(): 1})
	capacity.SetEscrowMembership("2", map[string]float64{shared.Address(): 0.5})

	if got, want := capacity.EscrowWeight("1", wiredModel), 55.0; got != want {
		t.Errorf("EscrowWeight(1) = %v, want %v", got, want)
	}
	if got, want := capacity.EscrowWeight("2", wiredModel), 45.0; got != want {
		t.Errorf("EscrowWeight(2) = %v, want %v", got, want)
	}
	capacity.SetEscrowMembership("1", map[string]float64{shared.Address(): 1, exclusive.Address(): 1})
	if got, want := capacity.EscrowWeight("1", wiredModel), 100.0; got != want {
		t.Errorf("EscrowWeight(1) with raw slot counts = %v, want %v -- the double count", got, want)
	}
}
