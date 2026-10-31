package scenarios

import (
	"testing"

	"devshard/cmd/gateway/internal/logcapture"
)

func judgedCandidate(reason, role string, serving ...servingEscrow) createCandidate {
	return createCandidate{
		createdID: "9", model: testModelID, role: role, reason: reason, reasonKnown: true,
		serving: serving, fullCost: 1_000, guaranteeSlots: 2, targetCount: 1, currentLabel: 7,
	}
}

func fullRegular(escrowID string) servingEscrow {
	return servingEscrow{escrowID: escrowID, role: regularRole, balance: 1_000, label: 7}
}

// Test flow:
//  1. Table-driven: one create per reason the gateway narrates, each beside the serving escrows that make its reason right or wrong.
//  2. Judge each create.
//  3. Assert only the creates whose reason the state contradicts are flagged, a guard create included when the escrows it could not read and its own plan had created already held the guarantee.
func TestCreatesAreJudgedByTheirNarratedReason(t *testing.T) {
	testCases := []struct {
		name      string
		candidate createCandidate
		violation bool
	}{
		{name: "a bridge regular beside a full regular", candidate: judgedCandidate("bridge", regularRole, fullRegular("1"))},
		{name: "an operator create beside a full regular", candidate: judgedCandidate("operator", regularRole, fullRegular("1"))},
		{name: "a guard create its plan counted one full escrow of two wanted for", candidate: func() createCandidate {
			candidate := judgedCandidate("guard", regularRole, fullRegular("1"), fullRegular("2"))
			candidate.planned = plannedFigures{found: true, fullCount: 1}
			return candidate
		}()},
		{name: "a guard create its plan counted two full escrows for", candidate: func() createCandidate {
			candidate := judgedCandidate("guard", regularRole)
			candidate.planned = plannedFigures{found: true, fullCount: 2}
			return candidate
		}(), violation: true},
		{name: "a guard create with no plan before it", candidate: judgedCandidate("guard", regularRole), violation: true},
		{name: "a second guard create of a plan whose first left a full unread escrow uncounted", candidate: func() createCandidate {
			candidate := judgedCandidate("guard", regularRole, fullRegular("1"), fullRegular("2"))
			candidate.planned, candidate.samePlan, candidate.unreadFull = plannedFigures{found: true}, []string{"2"}, []string{"1"}
			return candidate
		}(), violation: true},
		{name: "a guard create beside two full unread escrows", candidate: func() createCandidate {
			candidate := judgedCandidate("guard", regularRole, fullRegular("1"), fullRegular("2"))
			candidate.planned, candidate.unreadFull = plannedFigures{found: true}, []string{"1", "2"}
			return candidate
		}(), violation: true},
		{name: "a first guard create beside one full unread escrow of two wanted", candidate: func() createCandidate {
			candidate := judgedCandidate("guard", regularRole, fullRegular("1"))
			candidate.planned, candidate.unreadFull = plannedFigures{found: true}, []string{"1"}
			return candidate
		}()},
		{name: "a guard create whose plan's own create is the only other full escrow", candidate: func() createCandidate {
			candidate := judgedCandidate("guard", regularRole, fullRegular("2"))
			candidate.planned, candidate.samePlan, candidate.unreadFull = plannedFigures{found: true}, []string{"2"}, []string{"2"}
			return candidate
		}()},
		{name: "a capacity create after a money-short plan", candidate: func() createCandidate {
			candidate := judgedCandidate("capacity", regularRole, fullRegular("1"))
			candidate.planned = plannedFigures{found: true, moneyShort: true, need: 10, liquid: 20}
			return candidate
		}()},
		{name: "a capacity create the plan's money did not call for", candidate: func() createCandidate {
			candidate := judgedCandidate("capacity", regularRole, fullRegular("1"))
			candidate.planned = plannedFigures{found: true, need: 10, liquid: 20}
			return candidate
		}(), violation: true},
		{name: "a capacity create with no plan before it", candidate: judgedCandidate("capacity", regularRole), violation: true},
		{name: "a spread create with no current regular", candidate: judgedCandidate("spread", regularRole, servingEscrow{escrowID: "1", role: regularRole, label: 6})},
		{name: "a spread create with the spread met", candidate: judgedCandidate("spread", regularRole, fullRegular("1")), violation: true},
		{name: "a recovered create beside an escrow below full", candidate: judgedCandidate(recoveredReason, regularRole, servingEscrow{escrowID: "1", role: regularRole, balance: 999, label: 7})},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			detail := createViolation(testCase.candidate)

			if (detail != "") != testCase.violation {
				t.Fatalf("createViolation(%s) = %q, want violation %v", testCase.name, detail, testCase.violation)
			}
		})
	}
}

// Test flow:
//  1. Judge a create whose role is stored but whose reason line has not been logged, with the scenario running and then ended.
//  2. Assert it waits while the scenario runs and is flagged once it has ended.
func TestACreateWithNoRecordedReasonWaitsForTheRunToEnd(t *testing.T) {
	candidate := judgedCandidate("", regularRole)
	candidate.reasonKnown = false

	found, pending := judgeCreates([]createCandidate{candidate}, false)
	if len(found) != 0 || len(pending) != 1 {
		t.Fatalf("judgeCreates(running) = %v, %d pending, want none found and one pending", found, len(pending))
	}
	found, pending = judgeCreates([]createCandidate{candidate}, true)
	if len(found) != 1 || found[0].key != createReasonKey || len(pending) != 0 {
		t.Fatalf("judgeCreates(ended) = %v, %d pending, want one create reason violation", found, len(pending))
	}
}

// Test flow:
//  1. Build a journal of a funding plan, a capacity create after it, and a create recovered from its commitment.
//  2. Read the create lines and the plan before the capacity create.
//  3. Assert each create's reason and role, and the plan's figures.
func TestCreateReasonsAndPlansAreReadFromTheJournal(t *testing.T) {
	entries := []logcapture.Entry{
		{Msg: "escrow funding planned", Fields: []any{"model", testModelID, "money_short", true, "need", uint64(131_112), "liquid", uint64(40_000), "full_count", 1}},
		{Msg: "escrow created", Fields: []any{"escrow", "3", "model", testModelID, "role", "regular", "reason", "capacity"}},
		{Msg: "escrow recovered from commitment", Fields: []any{"escrow", "4", "model", testModelID, "role", "regular"}},
	}

	lines := createLinesOf(entries)
	planned := plannedBefore(entries, testModelID, lines["3"].index)

	if lines["3"].reason != "capacity" || lines["4"].reason != recoveredReason || lines["3"].role != regularRole || lines["4"].role != regularRole {
		t.Fatalf("createLinesOf() = %+v, want capacity for 3 and recovered for 4, both regular", lines)
	}
	if want := (plannedFigures{found: true, moneyShort: true, need: 131_112, liquid: 40_000, fullCount: 1}); planned != want {
		t.Fatalf("plannedBefore() = %+v, want %+v", planned, want)
	}
}

// Test flow:
//  1. Table-driven: a create whose row is stored, one whose row settlement dropped but whose create line names its role, and one with neither.
//  2. Resolve each create's role.
//  3. Assert the stored row wins, the create line stands in for a dropped row, and a create with neither stays unresolved.
func TestACreateWhoseRowWasDroppedTakesItsRoleFromItsCreateLine(t *testing.T) {
	rolesByID := map[string]string{"1": regularRole}
	lines := map[string]createLine{
		"1": {reason: "guard", role: tempRole},
		"2": {reason: "capacity", role: regularRole},
		"3": {reason: "capacity"},
	}
	for _, testCase := range []struct {
		name      string
		createdID string
		role      string
		known     bool
	}{
		{name: "a stored row", createdID: "1", role: regularRole, known: true},
		{name: "a row settlement dropped", createdID: "2", role: regularRole, known: true},
		{name: "a line that names no role", createdID: "3"},
		{name: "neither row nor line", createdID: "4"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			role, known := createdRole(testCase.createdID, rolesByID, lines)

			if role != testCase.role || known != testCase.known {
				t.Fatalf("createdRole(%s) = %q, %v, want %q, %v", testCase.createdID, role, known, testCase.role, testCase.known)
			}
		})
	}
}
