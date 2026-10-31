package scenarios

import (
	"fmt"
	"testing"
	"time"

	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/internal/logcapture"
)

// plannerBurstSpec drives two small escrows of a target-2 model, billed 5000 input tokens a request, through two bursts and ten quiet minutes.
func plannerBurstSpec() testSpec {
	spec := plannerSpec()
	spec.model.targetCount = 2
	spec.model.amount = 80_000
	spec.seeded = []seededEscrow{{amount: 80_000, role: "regular"}, {amount: 80_000, role: "regular"}}
	spec.behaviours = map[int]participantBehaviour{0: {inputTokens: 5000}, 1: {inputTokens: 5000}, 2: {inputTokens: 5000}, 3: {inputTokens: 5000}}
	spec.steps = []harnessStep{
		alignToTick{offset: time.Second},
		sendRequests{promptBytes: 19_900, maxTokens: 64, count: 12, sequential: true},
		advance{by: 2 * escrow.TickInterval},
		sendRequests{promptBytes: 19_900, maxTokens: 64, count: 12, sequential: true},
		advance{by: 10 * time.Minute},
	}
	return spec
}

func gaugeOf(harness *gatewayHarness, name, model string) (float64, bool) {
	families, err := harness.currentGateway().Telemetry().Registry().Gather()
	if err != nil {
		return 0, false
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "model" && label.GetValue() == model {
					return metric.GetGauge().GetValue(), true
				}
			}
		}
	}
	return 0, false
}

// Test flow:
//  1. Capture the log and boot one seeded full escrow of a model whose guarantee wants two full escrows.
//  2. Advance one tick and assert the planner narrated a guard create for the model.
//  3. Advance four more ticks and assert exactly one guard create landed and the exported guarantee metric reads zero.
func TestThePlannerNarratesAndExportsTheGuaranteeItRepairs(t *testing.T) {
	logged := logcapture.Install(t)
	spec := plannerSpec()
	spec.steps = []harnessStep{
		advance{by: escrow.TickInterval},
		expectThat{label: "the guard plan was narrated", verify: func(harness *gatewayHarness) error {
			harness.currentGateway().Journal().Flush()
			planned, found := logged.Find("escrow funding planned")
			if !found {
				return fmt.Errorf("no funding plan narrated")
			}
			if logcapture.Field(planned, "model") != testModelID || logcapture.Field(planned, "reasons") != "guard" {
				return fmt.Errorf("planned line fields = %v, want a guard create for %s", planned.Fields, testModelID)
			}
			return nil
		}},
		advance{by: 4 * escrow.TickInterval},
		expectThat{label: "the guarantee is repaired and exported", verify: func(harness *gatewayHarness) error {
			if guards := harness.createdWithReason(testModelID, "guard"); guards != 1 {
				return fmt.Errorf("createdWithReason(%s, guard) = %d, want 1", testModelID, guards)
			}
			guarantee, found := gaugeOf(harness, "devshard_gateway_funding_guarantee", testModelID)
			if !found || guarantee != 0 {
				return fmt.Errorf("devshard_gateway_funding_guarantee{model=%q} = %v (found %t), want 0", testModelID, guarantee, found)
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Run the planner burst: two seeded escrows of a target-2 model, every host billing 5000 input tokens, twelve requests, two ticks, twelve more, ten quiet minutes.
//  2. Assert every planner create kept to the bucket (`create reason` and `unsettled budget` held at every step).
func TestABurstKeepsEveryPlannerCreateWithinTheBucket(t *testing.T) {
	spec := plannerBurstSpec()
	spec.steps = append(spec.steps, expectPlannerCreatesWithinBucket("creates within the bucket", testModelID))
	runSteps(t, spec)
}
