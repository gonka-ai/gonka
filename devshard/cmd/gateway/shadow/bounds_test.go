package shadow

import (
	"math"
	"testing"

	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/scheduler"
	"devshard/user"
)

const otherModel = "model-b"

type fixedBounds map[string]ModelBounds

func (b fixedBounds) Of(model string) ModelBounds { return b[model] }

func profileWith(model string, promptTokens int, body string) *scheduler.RequestProfile {
	return &scheduler.RequestProfile{Model: model, InputTokens: promptTokens, Params: clientRequest(body)}
}

func drawnBudgets(t *testing.T, bounds ModelBounds, promptTokens int) (lowest, highest uint64) {
	t.Helper()
	runner := New(steppedClock(), fixedBounds{probeModel: bounds})
	lowest = math.MaxUint64
	for range drawCount {
		params, skip := runner.Params(profileWith(probeModel, promptTokens, clientBody), probeStartedAt)
		if skip != scheduler.ProbeSkipNone {
			t.Fatalf("skip = %v for bounds %+v and %d prompt tokens, want a probe", skip, bounds, promptTokens)
		}
		probe := params.(user.InferenceParams)
		members := decoded(t, probe.Prompt)
		maxTokens, minTokens := uint64(members["max_tokens"].(float64)), uint64(members["min_tokens"].(float64))
		if minTokens != maxTokens || maxTokens != probe.MaxTokens {
			t.Fatalf("max_tokens %d, min_tokens %d, MaxTokens %d, want one value", maxTokens, minTokens, probe.MaxTokens)
		}
		lowest, highest = min(lowest, maxTokens), max(highest, maxTokens)
	}
	return lowest, highest
}

// Test flow:
//  1. Draw many probes for a prompt that leaves 496 tokens of a 1000-token context after the chat template, with an output cap far above that.
//  2. Assert every draw has `min_tokens` equal to `max_tokens` and lies in [256, 496], and that the draws reach 496's neighbourhood.
func TestProbeBudgetIsBoundedByTheContextRoom(t *testing.T) {
	t.Parallel()
	room := uint64(496)
	prompt := 1000 - filters.ChatTemplateMinimumTokens - int(room)

	lowest, highest := drawnBudgets(t, ModelBounds{OutputCap: 4096, ContextLength: 1000}, prompt)

	if lowest < ProbeMinOutputTokens || highest > room || highest < room-20 {
		t.Errorf("draws span [%d, %d], want within [%d, %d] and reaching its top", lowest, highest, ProbeMinOutputTokens, room)
	}
}

// Test flow:
//  1. Draw many probes for a model whose output cap is 300 and whose context length is not known.
//  2. Assert every draw has `min_tokens` equal to `max_tokens` and lies in [256, 300].
func TestProbeBudgetIsBoundedByTheModelsOutputCap(t *testing.T) {
	t.Parallel()

	lowest, highest := drawnBudgets(t, ModelBounds{OutputCap: 300}, smallPromptTokens)

	if lowest < ProbeMinOutputTokens || highest > 300 || highest < 290 {
		t.Errorf("draws span [%d, %d], want within [%d, 300] and reaching its top", lowest, highest, ProbeMinOutputTokens)
	}
}

// Test flow:
//  1. Draw many probes for a model with a cap and a context far above 1024 and a short prompt.
//  2. Assert every draw has `min_tokens` equal to `max_tokens` and lies in [256, 1024], and that the draws reach both ends' neighbourhoods.
func TestProbeBudgetKeepsTheFixedBoundsWhenTheModelAllowsMore(t *testing.T) {
	t.Parallel()

	lowest, highest := drawnBudgets(t, ModelBounds{OutputCap: 4096, ContextLength: 32768}, smallPromptTokens)

	if lowest < ProbeMinOutputTokens || highest > ProbeMaxOutputTokens || lowest > ProbeMinOutputTokens+20 || highest < ProbeMaxOutputTokens-20 {
		t.Errorf("draws span [%d, %d], want [%d, %d]", lowest, highest, ProbeMinOutputTokens, ProbeMaxOutputTokens)
	}
}

// Test flow:
//  1. Ask for a probe where the room is exactly 256, and where it is one token less, and where the prompt fills or overfills the context, and where the output cap is 255 or the prompt count is negative with a cap of 255.
//  2. Assert the room of 256 gives a probe of exactly 256 tokens, and every other case is skipped as having no room and builds no params.
func TestProbeWithoutRoomForTheMinimumIsSkipped(t *testing.T) {
	t.Parallel()
	template := filters.ChatTemplateMinimumTokens
	cases := map[string]struct {
		bounds ModelBounds
		prompt int
	}{
		"room one under":     {ModelBounds{OutputCap: 4096, ContextLength: 1000}, 1000 - template - 255},
		"prompt fills":       {ModelBounds{OutputCap: 4096, ContextLength: 1000}, 1000 - template},
		"prompt overfills":   {ModelBounds{OutputCap: 4096, ContextLength: 1000}, 5000},
		"cap one under":      {ModelBounds{OutputCap: 255}, smallPromptTokens},
		"no cap known":       {ModelBounds{ContextLength: 32768}, smallPromptTokens},
		"negative prompt":    {ModelBounds{OutputCap: 255}, -50},
		"context under 256":  {ModelBounds{OutputCap: 4096, ContextLength: 100}, 0},
		"unlisted model cap": {ModelBounds{}, smallPromptTokens},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			params, skip := New(steppedClock(), fixedBounds{probeModel: tc.bounds}).Params(profileWith(probeModel, tc.prompt, clientBody), probeStartedAt)

			if skip != scheduler.ProbeSkipNoRoom || params != nil {
				t.Errorf("params = %v, skip = %v, want no params and ProbeSkipNoRoom", params, skip)
			}
		})
	}

	exact := ModelBounds{OutputCap: 4096, ContextLength: 1000}
	lowest, highest := drawnBudgets(t, exact, 1000-template-ProbeMinOutputTokens)
	if lowest != ProbeMinOutputTokens || highest != ProbeMinOutputTokens {
		t.Errorf("draws span [%d, %d] with room for exactly %d, want only %d", lowest, highest, ProbeMinOutputTokens, ProbeMinOutputTokens)
	}
}

// Test flow:
//  1. Give two models different bounds, one with room and one without.
//  2. Ask for a probe of a request routed to each.
//  3. Assert the bounds are read for the request's own model: one probe, one skip as having no room.
func TestProbeBoundsAreReadForTheRequestsModel(t *testing.T) {
	t.Parallel()
	runner := New(steppedClock(), fixedBounds{probeModel: {OutputCap: 4096, ContextLength: 32768}, otherModel: {OutputCap: 100}})

	_, served := runner.Params(profileWith(probeModel, smallPromptTokens, clientBody), probeStartedAt)
	_, skipped := runner.Params(profileWith(otherModel, smallPromptTokens, clientBody), probeStartedAt)

	if served != scheduler.ProbeSkipNone || skipped != scheduler.ProbeSkipNoRoom {
		t.Errorf("skips = %v and %v, want a probe for %s and no room for %s", served, skipped, probeModel, otherModel)
	}
}

// Test flow:
//  1. Ask for a probe of a request whose body is not JSON and whose model leaves no room.
//  2. Assert it is skipped as having no room, not as having no request, the check made before the body is read.
func TestNoRoomIsDecidedBeforeTheBodyIsRead(t *testing.T) {
	t.Parallel()
	unreadable := `not json`

	_, skip := New(steppedClock(), fixedBounds{probeModel: {OutputCap: 100}}).Params(profileWith(probeModel, smallPromptTokens, unreadable), probeStartedAt)

	if skip != scheduler.ProbeSkipNoRoom {
		t.Errorf("skip = %v, want ProbeSkipNoRoom", skip)
	}
}
