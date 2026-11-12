package app

import (
	"fmt"
	"maps"
	"slices"
	"testing"

	"devshard/bridge"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/scheduler"
)

type chainListing struct {
	unlisted map[string]bool
	asked    map[string]int
}

func (c *chainListing) GetHostInfo(address string) (*bridge.HostInfo, error) {
	c.asked[address]++
	if c.unlisted[address] {
		return nil, fmt.Errorf("participant %s: %w", address, bridge.ErrParticipantNotFound)
	}
	return &bridge.HostInfo{Address: address, URL: "http://" + address}, nil
}

type absentMarks map[string][]string

func (m absentMarks) AbsentHosts() map[string][]string { return maps.Clone(m) }

func (m absentMarks) ForgetAbsentHosts(escrowID string) { delete(m, escrowID) }

type drainingEscrows struct {
	live    map[string]string
	busy    map[string]bool
	retired map[string]string
}

func (e *drainingEscrows) Routable(escrowID string) (scheduler.Escrow, bool) {
	model, live := e.live[escrowID]
	return scheduler.Escrow{ID: escrowID, Model: model}, live
}

func (e *drainingEscrows) Candidates(model string) []scheduler.Escrow {
	var candidates []scheduler.Escrow
	for escrowID, liveModel := range e.live {
		if liveModel == model {
			candidates = append(candidates, scheduler.Escrow{ID: escrowID, Model: model})
		}
	}
	return candidates
}

func (e *drainingEscrows) Snapshot() []registry.EscrowState {
	var states []registry.EscrowState
	for escrowID, model := range e.live {
		states = append(states, registry.EscrowState{ID: escrowID, Model: model, Accepting: true})
	}
	for escrowID, model := range e.retired {
		if e.busy[escrowID] {
			states = append(states, registry.EscrowState{ID: escrowID, Model: model})
		}
	}
	return states
}

func (e *drainingEscrows) IsBusy(escrowID string) bool { return e.busy[escrowID] }

func (e *drainingEscrows) Retire(escrowID string) error {
	e.retired[escrowID] = e.live[escrowID]
	delete(e.live, escrowID)
	return nil
}

// Test flow:
//  1. Mark host h1 absent on an idle escrow, on a busy escrow its model's only one, on an escrow no longer live, and host h2, still unlisted, on a fourth.
//  2. List h1 again and run one probe.
//  3. Assert only the idle escrow was retired, its marks and the gone escrow's were forgotten, the busy and still-unlisted ones kept theirs, and each host was asked once.
func TestAnEscrowIsRetiredToReopenOnlyWhenItsModelStaysServed(t *testing.T) {
	hosts := &chainListing{unlisted: map[string]bool{"h2": true}, asked: map[string]int{}}
	marks := absentMarks{"idle": {"h1"}, "alone": {"h1"}, "gone": {"h1"}, "unlisted": {"h2"}}
	escrows := &drainingEscrows{
		live:    map[string]string{"idle": "m1", "alone": "m2", "unlisted": "m3"},
		busy:    map[string]bool{"alone": true, "unlisted": true},
		retired: map[string]string{},
	}

	retired := retireListedAbsentHosts(hosts, marks, escrows)

	if !retired || !slices.Equal(slices.Collect(maps.Keys(escrows.retired)), []string{"idle"}) {
		t.Fatalf("retired %v (reported %v), want only the idle escrow", escrows.retired, retired)
	}
	if want := (absentMarks{"alone": {"h1"}, "unlisted": {"h2"}}); !maps.EqualFunc(marks, want, slices.Equal) {
		t.Fatalf("marks left = %v, want %v", marks, want)
	}
	if want := map[string]int{"h1": 1, "h2": 1}; !maps.Equal(hosts.asked, want) {
		t.Fatalf("hosts asked = %v, want %v", hosts.asked, want)
	}
}

// Test flow:
//  1. Mark host h1 absent on two busy escrows of one model and host h2, still unlisted, on the third that serves it.
//  2. List h1 and run one probe, then a second while the retired escrow still drains.
//  3. Assert the first probe retired one of the two and the second retired none, so a model served only by marked escrows loses one at a time.
func TestABusyEscrowWithOnlyMarkedSiblingsIsRetiredToReopenOneAtATime(t *testing.T) {
	hosts := &chainListing{unlisted: map[string]bool{"h2": true}, asked: map[string]int{}}
	marks := absentMarks{"first": {"h1"}, "second": {"h1"}, "sibling": {"h2"}}
	escrows := &drainingEscrows{
		live:    map[string]string{"first": "m1", "second": "m1", "sibling": "m1"},
		busy:    map[string]bool{"first": true, "second": true, "sibling": true},
		retired: map[string]string{},
	}

	retireListedAbsentHosts(hosts, marks, escrows)
	afterFirst := len(escrows.retired)
	retireListedAbsentHosts(hosts, marks, escrows)

	if afterFirst != 1 || len(escrows.retired) != 1 {
		t.Fatalf("retired after each probe = %d, %d; want 1, 1", afterFirst, len(escrows.retired))
	}
}

// Test flow:
//  1. Mark host h1 absent on three busy escrows of one model that an unmarked escrow also serves, while a fifth escrow of the model drains after rotation.
//  2. List h1 and run one probe.
//  3. Assert the probe retired all three marked escrows and forgot their marks, and the unmarked escrow still routes.
func TestEveryMarkedEscrowIsRetiredToReopenAtOnceWhileAnUnmarkedOneServesItsModel(t *testing.T) {
	hosts := &chainListing{asked: map[string]int{}}
	marks := absentMarks{"first": {"h1"}, "second": {"h1"}, "third": {"h1"}}
	escrows := &drainingEscrows{
		live:    map[string]string{"first": "m1", "second": "m1", "third": "m1", "clean": "m1"},
		busy:    map[string]bool{"first": true, "second": true, "third": true, "clean": true, "rotated": true},
		retired: map[string]string{"rotated": "m1"},
	}

	retired := retireListedAbsentHosts(hosts, marks, escrows)

	if want := []string{"first", "rotated", "second", "third"}; !retired || !slices.Equal(slices.Sorted(maps.Keys(escrows.retired)), want) {
		t.Fatalf("retired %v (reported %v), want %v", slices.Sorted(maps.Keys(escrows.retired)), retired, want)
	}
	if len(marks) != 0 {
		t.Fatalf("marks left = %v, want none", marks)
	}
	if _, routes := escrows.Routable("clean"); !routes {
		t.Fatal("the unmarked escrow no longer routes")
	}
}
