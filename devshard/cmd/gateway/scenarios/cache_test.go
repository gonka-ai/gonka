package scenarios

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	cacheHitsMetricName = "devshard_gateway_cache_hits_total"
	repeatedRequestBody = `{"model":"scenario-model","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"the same question every time"}]}`
)

type cacheProbe struct{ hits []float64 }

func cacheHits(harness *gatewayHarness) (float64, error) {
	families, err := harness.currentGateway().Telemetry().Registry().Gather()
	if err != nil {
		return 0, err
	}
	for _, family := range families {
		if family.GetName() == cacheHitsMetricName {
			return family.GetMetric()[0].GetCounter().GetValue(), nil
		}
	}
	return 0, fmt.Errorf("metric %s not exposed", cacheHitsMetricName)
}

type sendRepeatedRequest struct{ probe *cacheProbe }

func (step sendRepeatedRequest) apply(harness *gatewayHarness) {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(repeatedRequestBody))
	request.Header.Set("Content-Type", "application/json")
	harness.currentGateway().Handler().ServeHTTP(httptest.NewRecorder(), request)
	hits, err := cacheHits(harness)
	if err != nil {
		harness.t.Fatalf("reading cache hits: %v", err)
	}
	step.probe.hits = append(step.probe.hits, hits)
}

// Test flow:
//  1. Make validators 1, 2 and 3 vote every validation invalid, then send one request twice.
//  2. Assert the second is served from the cache, so the first was stored.
//  3. Send rounds of other requests so the invalid votes reach the session, then send the first request a third time.
//  4. Assert the third is not served from the cache: the invalid vote removed the entry and the request ran a race again.
func TestAReplyVotedInvalidIsNotServedFromTheCacheAgain(t *testing.T) {
	spec := defaultSpec()
	spec.validationRate = everyValidatorChecks
	spec.behaviours = map[int]participantBehaviour{
		1: {votesInvalid: true},
		2: {votesInvalid: true},
		3: {votesInvalid: true},
	}
	probe := &cacheProbe{}
	spec.steps = append([]harnessStep{sendRepeatedRequest{probe}, sendRepeatedRequest{probe}}, requestRounds(8, 4, 10*time.Second)...)
	spec.steps = append(spec.steps,
		sendRepeatedRequest{probe},
		expectThat{label: "the voted-invalid reply was raced again", verify: func(harness *gatewayHarness) error {
			if want := []float64{0, 1, 1}; !slices.Equal(probe.hits, want) {
				return fmt.Errorf("cache hits after each repeated request = %v, want %v", probe.hits, want)
			}
			return nil
		}},
	)
	runSteps(t, spec)
}
