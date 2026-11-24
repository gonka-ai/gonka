package limits

import (
	"context"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
)

// Test flow:
//  1. Map `config.Defaults().Limits` through `GatewayConfigFromLimits`.
//  2. Assert MaxConcurrent, MaxInputTokens and AcquireWait each match the corresponding defaults field.
func TestGatewayConfigFromLimits_MapsFieldsFromDefaults(t *testing.T) {
	t.Parallel()
	limits := config.Defaults().Limits

	got := GatewayConfigFromLimits(limits)

	if got.MaxConcurrent != limits.Concurrency.MaxRequests {
		t.Errorf("MaxConcurrent = %d, want %d", got.MaxConcurrent, limits.Concurrency.MaxRequests)
	}
	if got.MaxInputTokens != limits.MaxInputTokensInFlight {
		t.Errorf("MaxInputTokens = %d, want %d", got.MaxInputTokens, limits.MaxInputTokensInFlight)
	}
	if want := time.Duration(limits.AdmissionQueueWaitMS) * time.Millisecond; got.AcquireWait != want {
		t.Errorf("AcquireWait = %v, want %v", got.AcquireWait, want)
	}
}

// Test flow:
//  1. Resolve a config whose network sets admission caps and wait, and a model that overrides its concurrency cap and wait.
//  2. Map it through `GatewayConfigFromConfig`.
//  3. Assert the network caps are the network values, the named model carries its own cap and wait, and every field it leaves alone is the network's.
func TestGatewayConfigFromConfig_MapsPerModelCaps(t *testing.T) {
	t.Parallel()
	maxConcurrent, queueWaitMS := int64(7), int64(1_500)
	configuration := config.Defaults()
	configuration.Limits.Concurrency.MaxRequests = 40
	configuration.Limits.MaxInputTokensInFlight = 9_000
	configuration.Limits.AdmissionQueueWaitMS = 20_000
	if err := configuration.ResolveModels(map[string]config.ModelSettings{
		"modelA": {MaxConcurrentRequests: &maxConcurrent, AdmissionQueueWaitMS: &queueWaitMS},
	}); err != nil {
		t.Fatalf("ResolveModels() = %v", err)
	}

	got := GatewayConfigFromConfig(&configuration)

	if got.MaxConcurrent != 40 || got.AcquireWait != 20*time.Second {
		t.Errorf("network caps = %d / %v, want 40 / 20s", got.MaxConcurrent, got.AcquireWait)
	}
	own := got.ByModel["modelA"]
	if own.MaxConcurrent != 7 || own.AcquireWait != 1_500*time.Millisecond {
		t.Errorf("ByModel[modelA] caps = %d / %v, want 7 / 1.5s", own.MaxConcurrent, own.AcquireWait)
	}
	if own.MaxInputTokens != 9_000 {
		t.Errorf("ByModel[modelA].MaxInputTokens = %d, want the network 9000", own.MaxInputTokens)
	}
	if _, named := got.ByModel["modelB"]; named {
		t.Error("ByModel names modelB, which carries no settings")
	}
}

// Test flow:
//  1. Map `config.Defaults().Limits` through `ParticipantConfigFromLimits`.
//  2. Assert the input and output pricing bounds match the configured host windows.
//  3. Assert the fallback context and output token counts match the model length and token cap defaults.
//  4. Assert the congestion factors, slack, failure threshold and breaker open durations all match the defaults.
func TestParticipantConfigFromLimits_MapsFieldsFromDefaults(t *testing.T) {
	t.Parallel()
	limits := config.Defaults().Limits

	got := ParticipantConfigFromLimits(limits)

	wantInput := RequestBounds{
		Min:     limits.HostWindows.Input.MinRequests,
		Initial: limits.HostWindows.Input.InitialRequests,
	}
	if got.Pricing.Input != wantInput {
		t.Errorf("Pricing.Input = %+v, want %+v", got.Pricing.Input, wantInput)
	}
	wantOutput := RequestBounds{
		Min:     limits.HostWindows.Output.MinRequests,
		Initial: limits.HostWindows.Output.InitialRequests,
	}
	if got.Pricing.Output != wantOutput {
		t.Errorf("Pricing.Output = %+v, want %+v", got.Pricing.Output, wantOutput)
	}
	if got.Pricing.FallbackContextTokens != limits.FallbackMaxModelLen {
		t.Errorf("FallbackContextTokens = %d, want %d: prefill is counted in the tokens of the model's own context length",
			got.Pricing.FallbackContextTokens, limits.FallbackMaxModelLen)
	}
	if got.Pricing.FallbackOutputTokens != limits.MaxTokensCap {
		t.Errorf("FallbackOutputTokens = %d, want %d: decode is counted in the tokens of the model's own output cap",
			got.Pricing.FallbackOutputTokens, limits.MaxTokensCap)
	}
	wantFactors := CongestionFactors{
		Soft:   limits.Congestion.BetaSoft,
		Hard:   limits.Congestion.BetaHard,
		Severe: limits.Congestion.BetaSevere,
		Cross:  limits.Congestion.BetaCross,
	}
	if got.Factors != wantFactors {
		t.Errorf("Factors = %+v, want %+v", got.Factors, wantFactors)
	}
	if got.Slack != limits.Congestion.Slack {
		t.Errorf("Slack = %v, want %v", got.Slack, limits.Congestion.Slack)
	}
	if got.AfterFailures != limits.HostCutoff.AfterFailures {
		t.Errorf("AfterFailures = %d, want %d", got.AfterFailures, limits.HostCutoff.AfterFailures)
	}
	if got.BaseOpen != 5*time.Second {
		t.Errorf("BaseOpen = %v, want 5s (default BaseMS=5000)", got.BaseOpen)
	}
	if got.MaxOpen != 60*time.Second {
		t.Errorf("MaxOpen = %v, want 60s (default MaxMS=60000)", got.MaxOpen)
	}
}

// Test flow:
//  1. Configure distinct input and output host windows, and a named model with its own context length, token cap and input window.
//  2. Map the configuration through `ParticipantConfigFromConfig`.
//  3. Assert the network pricing keeps the input and output bounds, not shared.
//  4. Assert the model's own pricing carries its pinned context length, its own output cap as the fallback, and its own window.
func TestParticipantConfigFromConfig_PricesEachModelFromItsOwnSettings(t *testing.T) {
	t.Parallel()
	namedContext, namedCap, namedInitial := int64(32_768), int64(1_024), int64(4)
	configuration := config.Defaults()
	configuration.Limits.HostWindows.Input = config.RequestWindow{MinRequests: 1, InitialRequests: 2}
	configuration.Limits.HostWindows.Output = config.RequestWindow{MinRequests: 3, InitialRequests: 6}
	if err := configuration.ResolveModels(map[string]config.ModelSettings{
		"model-a": {MaxModelLen: &namedContext, MaxTokensCap: &namedCap, HostInputWindowInitialRequests: &namedInitial},
	}); err != nil {
		t.Fatalf("ResolveModels() = %v", err)
	}

	got := ParticipantConfigFromConfig(&configuration)

	if got.Pricing.Input != (RequestBounds{Min: 1, Initial: 2}) || got.Pricing.Output != (RequestBounds{Min: 3, Initial: 6}) {
		t.Errorf("network pricing = %+v / %+v, want {1 2} and {3 6}: prefill and decode have their own request counts", got.Pricing.Input, got.Pricing.Output)
	}
	own := got.ByModel["model-a"].Pricing
	if own.ContextTokensByModel["model-a"] != namedContext {
		t.Errorf("ByModel[model-a] context = %d, want %d: a model's own context length is what its prefill window counts in", own.ContextTokensByModel["model-a"], namedContext)
	}
	if own.FallbackOutputTokens != namedCap {
		t.Errorf("ByModel[model-a] output tokens = %d, want %d: a model's own output cap is what its decode window counts in", own.FallbackOutputTokens, namedCap)
	}
	if own.Input != (RequestBounds{Min: 1, Initial: namedInitial}) {
		t.Errorf("ByModel[model-a].Input = %+v, want {Min:1 Initial:%d}", own.Input, namedInitial)
	}
}

// Test flow:
//  1. Set `Perf.HostStalenessSeconds` to 90 on a default configuration.
//  2. Map it through `ParticipantConfigFromConfig`.
//  3. Assert IdleEviction equals the 90-second staleness window.
//  4. Assert the pricing windows still match the plain `ParticipantConfigFromLimits` mapping, unaffected by the eviction setting.
func TestParticipantConfigFromConfig_ForgetsIdlePairsOnThePerfStalenessWindow(t *testing.T) {
	t.Parallel()
	configuration := config.Defaults()
	configuration.Perf.HostStalenessSeconds = 90

	got := ParticipantConfigFromConfig(&configuration)

	if got.IdleEviction != 90*time.Second {
		t.Fatalf("IdleEviction = %v, want 90s from perf_host_staleness_seconds", got.IdleEviction)
	}
	if got.Pricing.Input != ParticipantConfigFromLimits(configuration.Limits).Pricing.Input {
		t.Fatal("ParticipantConfigFromConfig changed the windows it was only meant to add an eviction window to")
	}
}

// Test flow:
//  1. Build a `GatewayLimiter`, a `ParticipantLimiter` and a `Capacity`, all from the same default limits.
//  2. Update `Capacity` with current and full chain weights for "modelA" and assert its scale factor is 0.8.
//  3. Acquire and release one request on the gateway limiter under that scale factor.
//  4. Acquire on the participant limiter up to its initial window size and assert every one is admitted.
//  5. Assert one more acquire beyond the window is refused.
func TestCapacityGatewayParticipantLimiterComposeEndToEnd(t *testing.T) {
	t.Parallel()
	limits := config.Defaults().Limits
	gatewayLimiter := NewGatewayLimiter(GatewayConfigFromLimits(limits))
	participantLimiter := NewParticipantLimiter(ParticipantConfigFromLimits(limits), fixedNow(testEpoch))
	capacity := NewCapacity(nil)

	capacity.Update(chain.PhaseSnapshot{
		CurrentWeightsByModel: map[string]map[string]float64{
			"modelA": {"hostA": 60, "hostB": 20},
		},
		FullWeightsByModel: map[string]map[string]float64{
			"modelA": {"hostA": 80, "hostB": 20},
		},
	})

	scale := capacity.ModelWeights("modelA", false).ScaleFactor
	if scale != 0.8 {
		t.Fatalf("Capacity.ModelWeights(modelA).ScaleFactor = %v, want 0.8 (80 current / 100 full)", scale)
	}
	modelCapacity := ModelCapacity{ScaleFactor: scale}

	ctx := context.Background()
	if err := gatewayLimiter.AcquireForModel(ctx, "modelA", 128, modelCapacity); err != nil {
		t.Fatalf("GatewayLimiter.AcquireForModel under cap = %v, want nil", err)
	}
	gatewayLimiter.ReleaseForModel("modelA", 128)

	request := TokenCost{Input: limits.FallbackMaxModelLen, Output: limits.MaxTokensCap}
	admitted := limits.HostWindows.Input.InitialRequests
	for i := range admitted {
		if _, ok := participantLimiter.Acquire("hostA", "modelA", request); ok != AdmissionOpen {
			t.Fatalf("ParticipantLimiter.Acquire call %d = %s, want open (the initial window fits %d of them)", i+1, ok, admitted)
		}
	}
	if _, ok := participantLimiter.Acquire("hostA", "modelA", request); ok == AdmissionOpen {
		t.Fatal("ParticipantLimiter.Acquire beyond the initial window = true, want false (per-host window must gate the host)")
	}
}
