package scenarios

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/escrow"
)

const (
	seedRuns         = 16
	seedRunsShort    = 4
	seedWallBudget   = 60 * time.Second
	seedRequestsMost = 8
	seedEpochSettle  = 2 * time.Minute
)

type seedActionKind int

const (
	seedSmallRequests seedActionKind = iota
	seedLargeRequests
	seedAdvanceTicks
	seedInvalidVotes
	seedStall
	seedHeal
	seedRestart
	seedBridgeWindow
	seedStartPoC
	seedSwitchEpoch
	seedWalletTopUp
	seedActionKinds
)

var seedWeights = [seedActionKinds]int{40, 12, 25, 5, 3, 4, 2, 1, 1, 1, 1}

type seedAction struct {
	Kind        seedActionKind
	Count       int
	Model       int
	Participant int
}

type seedShape struct {
	Targets   []int
	GroupSize int
}

// seedEpochMove moves the chain and lets two minutes pass, as a real chain takes time between its phases.
type seedEpochMove struct{ move func(*fakeChain) }

func (step seedEpochMove) apply(harness *gatewayHarness) {
	step.move(harness.chain)
	time.Sleep(seedEpochSettle)
}

func drawAction(random *rand.Rand, shape seedShape) seedAction {
	total := 0
	for _, weight := range seedWeights {
		total += weight
	}
	pick, kind := random.IntN(total), seedActionKind(0)
	for ; kind < seedActionKinds-1 && pick >= seedWeights[kind]; kind++ {
		pick -= seedWeights[kind]
	}
	return seedAction{Kind: kind, Count: 1 + random.IntN(seedRequestsMost), Model: random.IntN(len(shape.Targets)), Participant: random.IntN(shape.GroupSize)}
}

func generateSeed(seed uint64) (seedShape, []seedAction) {
	random := rand.New(rand.NewPCG(seed, 0))
	shape := seedShape{GroupSize: 4}
	if random.IntN(8) == 0 {
		shape.GroupSize = 16
	}
	for range 1 + random.IntN(3) {
		shape.Targets = append(shape.Targets, 1+random.IntN(6))
	}
	actions := make([]seedAction, 50+random.IntN(151))
	for index := range actions {
		actions[index] = drawAction(random, shape)
	}
	return shape, actions
}

func decodeSeed(data []byte) (seedShape, []seedAction) {
	shape := seedShape{GroupSize: 4, Targets: []int{1}}
	if len(data) > 0 {
		shape.Targets = make([]int, 1+int(data[0])%3)
		for index := range shape.Targets {
			shape.Targets[index] = 1 + (int(data[0])/3+index)%6
		}
		data = data[1:]
	}
	var actions []seedAction
	for len(data) >= 2 && len(actions) < 200 {
		actions = append(actions, seedAction{
			Kind: seedActionKind(int(data[0]) % int(seedActionKinds)), Count: 1 + int(data[1])%seedRequestsMost,
			Model: int(data[1]) % len(shape.Targets), Participant: int(data[1]) % shape.GroupSize,
		})
		data = data[2:]
	}
	return shape, actions
}

func seedModelID(index int) string {
	if index == 0 {
		return testModelID
	}
	return fmt.Sprintf("%s-%d", testModelID, index+1)
}

func seedSpec(shape seedShape, actions []seedAction, violation *string) testSpec {
	spec := plannerSpec()
	spec.participants, spec.groupSize = max(4, shape.GroupSize), shape.GroupSize
	spec.model.targetCount = shape.Targets[0]
	spec.seeded = []seededEscrow{{amount: 1_000_000, role: regularRole}}
	for index, target := range shape.Targets[1:] {
		model := modelSpec{id: seedModelID(index + 1), maxModelLen: 8_192, amount: 1_000_000, targetCount: target, tempCount: 1}
		spec.otherModels = append(spec.otherModels, model)
		spec.seeded = append(spec.seeded, seededEscrow{amount: 1_000_000, role: regularRole, model: model.id})
	}
	if shape.GroupSize == 4 {
		spec.validationRate = everyValidatorChecks
	}
	spec.violationSink = violation
	for _, action := range actions {
		spec.steps = append(spec.steps, seedStep(action, shape))
	}
	return spec
}

func seedStep(action seedAction, shape seedShape) harnessStep {
	model := seedModelID(action.Model % len(shape.Targets))
	participant := []int{action.Participant % shape.GroupSize}
	switch action.Kind {
	case seedSmallRequests:
		return sendRequests{promptBytes: 200, maxTokens: 64, count: action.Count, model: model}
	case seedLargeRequests:
		return sendRequests{promptBytes: 30_000, maxTokens: 64, count: 1 + action.Count%3, model: model}
	case seedInvalidVotes:
		return changeParticipants{indexes: participant, change: func(behaviour *participantBehaviour) { behaviour.votesInvalid = true }}
	case seedStall:
		return changeParticipants{indexes: participant, change: func(behaviour *participantBehaviour) { behaviour.stall = true }}
	case seedHeal:
		return changeParticipants{indexes: participant, change: func(behaviour *participantBehaviour) { *behaviour = participantBehaviour{} }}
	case seedRestart:
		return restartGateway{}
	case seedBridgeWindow:
		return seedEpochMove{move: func(blockchain *fakeChain) {
			if epoch := blockchain.snapshotEpoch(); epoch.phase == chain.EpochPhaseInference && epoch.blockHeight < epoch.setNewValidators-200 {
				blockchain.moveToHeight(epoch.setNewValidators - 200)
			}
		}}
	case seedStartPoC:
		return seedEpochMove{move: func(blockchain *fakeChain) {
			if epoch := blockchain.snapshotEpoch(); epoch.phase == chain.EpochPhaseInference {
				blockchain.startPoC()
			}
		}}
	case seedSwitchEpoch:
		return seedEpochMove{move: func(blockchain *fakeChain) {
			if epoch := blockchain.snapshotEpoch(); epoch.phase != chain.EpochPhaseInference {
				blockchain.setNewValidators(epoch.setNewValidators+1_000, epoch.setNewValidators+1_100)
			}
		}}
	case seedWalletTopUp:
		return fundWallet{amount: 5_000_000}
	}
	return advance{by: time.Duration(action.Count) * escrow.TickInterval}
}

func seedRunCount(t *testing.T) int {
	if raw := os.Getenv("GATEWAY_SCENARIO_SEEDS"); raw != "" {
		count, err := strconv.Atoi(raw)
		if err != nil || count < 1 {
			t.Fatalf("GATEWAY_SCENARIO_SEEDS = %q, want a positive count", raw)
		}
		return count
	}
	if testing.Short() {
		return seedRunsShort
	}
	return seedRuns
}

// Test flow:
//  1. For each seed, generate a fleet shape (1-3 models, targets 1-6, group 4 or 16) and 50-200 weighted actions, and run them as one scenario with every invariant after every step.
//  2. On a failure, shrink the actions to the fewest that still break the same invariant and fail with them as a Go literal.
//  3. Without the race detector, assert the default run of sixteen seeds kept to its sixty-second wall budget.
func TestRandomSeedsKeepEveryInvariant(t *testing.T) {
	runs, started := seedRunCount(t), time.Now()
	for seed := uint64(1); seed <= uint64(runs); seed++ {
		shape, actions := generateSeed(seed)
		var violation string
		if t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) { runSteps(t, seedSpec(shape, actions, &violation)) }) {
			continue
		}
		minimal := shrinkActions(t, shape, actions, violation)
		t.Fatalf("seed %d breaks %s; shape %#v; minimal actions for a regression test:\n%#v", seed, violation, shape, minimal)
	}
	if elapsed := time.Since(started); !raceEnabled && runs == seedRuns && elapsed > seedWallBudget {
		t.Fatalf("sixteen seeds took %s, want at most %s", elapsed, seedWallBudget)
	}
}

// shrinkActions drops halves, then quarters, then single actions, keeping every cut that still breaks the same invariant.
func shrinkActions(t *testing.T, shape seedShape, actions []seedAction, violation string) []seedAction {
	current := actions
	for _, parts := range []int{2, 4} {
		size := len(current) / parts
		for start := 0; size > 0 && start+size <= len(current); {
			candidate := slices.Delete(slices.Clone(current), start, start+size)
			if stillBreaks(t, shape, candidate, violation) {
				current = candidate
				continue
			}
			start += size
		}
	}
	for index := 0; index < len(current); {
		candidate := slices.Delete(slices.Clone(current), index, index+1)
		if stillBreaks(t, shape, candidate, violation) {
			current = candidate
			continue
		}
		index++
	}
	return current
}

func stillBreaks(t *testing.T, shape seedShape, actions []seedAction, violation string) bool {
	var found string
	passed := t.Run("shrink", func(t *testing.T) { runSteps(t, seedSpec(shape, actions, &found)) })
	return !passed && found == violation
}

// Test flow:
//  1. Decode the fuzzer's bytes into a fleet shape and a list of actions, starting from one fixed seed input.
//  2. Run them as one scenario and assert every invariant holds after every step.
func FuzzEscrowLifecycle(f *testing.F) {
	f.Add([]byte{5, 1, 3, 2, 8, 0, 4, 11})
	f.Fuzz(func(t *testing.T, data []byte) {
		shape, actions := decodeSeed(data)
		runSteps(t, seedSpec(shape, actions, nil))
	})
}

// Test flow:
//  1. Generate seed 7 twice and decode a fixed byte string twice.
//  2. Assert each gives the same shape and actions both times, the shape stays inside its ranges, and the actions number 50 to 200.
func TestSeedsAreDeterministicAndInsideTheirRanges(t *testing.T) {
	firstShape, firstActions := generateSeed(7)
	secondShape, secondActions := generateSeed(7)
	if !slices.Equal(firstShape.Targets, secondShape.Targets) || firstShape.GroupSize != secondShape.GroupSize || !slices.Equal(firstActions, secondActions) {
		t.Fatalf("generateSeed(7) differs between two calls")
	}
	if len(firstShape.Targets) < 1 || len(firstShape.Targets) > 3 || (firstShape.GroupSize != 4 && firstShape.GroupSize != 16) {
		t.Fatalf("generateSeed(7) shape = %+v, want 1-3 models and a group of 4 or 16", firstShape)
	}
	for _, target := range firstShape.Targets {
		if target < 1 || target > 6 {
			t.Fatalf("generateSeed(7) target = %d, want 1-6", target)
		}
	}
	if len(firstActions) < 50 || len(firstActions) > 200 {
		t.Fatalf("generateSeed(7) = %d actions, want 50-200", len(firstActions))
	}
	decodedShape, decodedActions := decodeSeed([]byte{5, 1, 3, 2, 8, 0, 4, 11})
	againShape, againActions := decodeSeed([]byte{5, 1, 3, 2, 8, 0, 4, 11})
	if !slices.Equal(decodedShape.Targets, againShape.Targets) || !slices.Equal(decodedActions, againActions) || len(decodedActions) != 3 {
		t.Fatalf("decodeSeed() = %+v %v, want the same three actions twice", decodedShape, decodedActions)
	}
}

// Test flow:
//  1. Run the actions seed 10 shrank to over two models with targets 3 and 4: start PoC, send seven requests on the second model, then switch epochs and start PoC twice more, so the latest epoch reaches 10.
//  2. Assert every invariant holds after every step: the ledger's retention of two epochs prunes the retired escrows of epoch 7, and `ledger match` does not read that pruning as a ledger that never opened them.
//  3. Assert the ledger did prune a settled escrow that was charged.
func TestTheLedgerPrunesSettledEscrowsPastItsRetention(t *testing.T) {
	shape := seedShape{Targets: []int{3, 4}, GroupSize: 4}
	actions := []seedAction{
		{Kind: seedStartPoC, Count: 3, Model: 0, Participant: 3},
		{Kind: seedSmallRequests, Count: 7, Model: 1, Participant: 1},
		{Kind: seedSwitchEpoch, Count: 6, Model: 0, Participant: 3},
		{Kind: seedStartPoC, Count: 5, Model: 1, Participant: 3},
		{Kind: seedSwitchEpoch, Count: 1, Model: 0, Participant: 2},
		{Kind: seedStartPoC, Count: 3, Model: 0, Participant: 2},
	}
	spec := seedSpec(shape, actions, nil)
	spec.steps = append(spec.steps, expectThat{label: "the ledger pruned a settled escrow past its retention", verify: func(harness *gatewayHarness) error {
		book := harness.currentGateway().Nonces().Book()
		for _, model := range harness.spec.allModels() {
			for _, record := range harness.chain.escrowsOf(model.id) {
				if _, known := book.MoneyTotals(formatEscrowID(record.id)); record.settled && record.settledCosts > 0 && !known {
					return nil
				}
			}
		}
		return fmt.Errorf("MoneyTotals() knows every settled escrow that was charged, want one pruned by the retention")
	}})
	runSteps(t, spec)
}

// Test flow:
//  1. Boot the planner over one seeded escrow, whose guarantee of two full escrows creates a guard escrow at boot, and run no step at all.
//  2. Assert the scenario ends clean: the boot create is stored with its role and narrated with its reason before the shutdown closes the store.
func TestHarnessJudgesABootCreateWhenNoStepRuns(t *testing.T) {
	runSteps(t, plannerSpec())
}

// Test flow:
//  1. Run the actions seed 18 shrank to over three models with targets 4, 5 and 3: advance three, three and five ticks, then send eight requests on the second model as the run's last step.
//  2. Assert every invariant holds after every step and at rest: the answered records finish on their sessions up to twenty seconds after their answers, and the ledger is compared once a whole sweep interval passed with no session's money moving.
func TestTheLedgerIsComparedOnceTheSessionsStandStill(t *testing.T) {
	shape := seedShape{Targets: []int{4, 5, 3}, GroupSize: 4}
	actions := []seedAction{
		{Kind: seedAdvanceTicks, Count: 3, Model: 2, Participant: 3},
		{Kind: seedAdvanceTicks, Count: 3, Model: 0, Participant: 1},
		{Kind: seedAdvanceTicks, Count: 5, Model: 2, Participant: 3},
		{Kind: seedSmallRequests, Count: 8, Model: 1, Participant: 2},
	}
	runSteps(t, seedSpec(shape, actions, nil))
}

// Test flow:
//  1. Run the actions seed 36 reduced to: stall participant 3, send eight requests, and restart the gateway as the last step, so the old engine still owes timeout votes whose timers lie past the scenario's end.
//  2. Assert the scenario ends clean instead of deadlocking its bubble: the harness waits out the votes the replaced engine owes, as it does for the running one.
func TestHarnessEndsAfterARestartThatLeftVotesOwed(t *testing.T) {
	shape := seedShape{Targets: []int{1}, GroupSize: 4}
	actions := []seedAction{
		{Kind: seedStall, Count: 8, Model: 0, Participant: 3},
		{Kind: seedSmallRequests, Count: 8, Model: 0, Participant: 1},
		{Kind: seedRestart, Count: 8, Model: 0, Participant: 0},
	}
	runSteps(t, seedSpec(shape, actions, nil))
}
