package limits

import "testing"

// Test flow:
//  1. Build a limiter whose network cut-off opens after 3 transport faults and whose model-b opens after 1.
//  2. Answer one transport fault on model-a and on model-b for the same participant.
//  3. Assert the participant is still admitted on model-a and refused on model-b: the cut-off count is the model's own.
func TestTheCutoffCountIsTheModelsOwn(t *testing.T) {
	configuration := testConfig()
	configuration.ByModel = map[string]ParticipantConfig{"model-b": testConfig()}
	own := configuration.ByModel["model-b"]
	own.AfterFailures = 1
	configuration.ByModel["model-b"] = own
	limiter := newTestLimiter(configuration, fixedNow(testEpoch))

	limiter.answered("participant-a", "model-a", TransportFault)
	limiter.answered("participant-a", "model-b", TransportFault)

	if !limiter.admits("participant-a", "model-a") {
		t.Error("participant-a is cut off on model-a after 1 fault under the network count of 3")
	}
	if limiter.admits("participant-a", "model-b") {
		t.Error("participant-a is admitted on model-b after 1 fault under its own count of 1")
	}
}

// Test flow:
//  1. Build a limiter whose network host window starts at 4 requests and whose model-b starts at 2.
//  2. Take leases for one participant on each model until the limiter refuses.
//  3. Assert model-a admits 4 and model-b admits 2: each model's windows are priced from its own settings.
func TestTheWindowsAreSizedFromTheModelsOwnPricing(t *testing.T) {
	configuration := testConfig()
	own := testConfig()
	own.Pricing.Input = RequestBounds{Min: 1, Initial: 2}
	own.Pricing.Output = RequestBounds{Min: 1, Initial: 2}
	configuration.ByModel = map[string]ParticipantConfig{"model-b": own}
	limiter := newTestLimiter(configuration, fixedNow(testEpoch))

	for model, want := range map[string]int{"model-a": 4, "model-b": 2} {
		admitted := 0
		for limiter.admits("participant-a", model) {
			admitted++
			if admitted > want {
				break
			}
		}
		if admitted != want {
			t.Errorf("%s admitted %d requests, want %d", model, admitted, want)
		}
	}
}
