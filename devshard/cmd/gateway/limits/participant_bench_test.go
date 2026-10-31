package limits

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
)

const (
	benchModel   = "model-bench"
	benchHosts   = 16
	benchNetwork = 32
)

var benchClock = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// benchCost prices a benchmark request at one token per dimension, so a window of eight admits eight.
var benchCost = TokenCost{Input: 1, Output: 1}

func benchResult(participant string) Result {
	return Result{Participant: participant, Model: benchModel, Verdict: Success, Carried: benchCost}
}

// The sinks keep what a benchmark measured from being optimised away.
var (
	windowSink []HostWindow
	floatSink  float64
)

// benchParticipant is bech32-shaped, so map keys hash over a realistic length.
func benchParticipant(index int) string {
	return fmt.Sprintf("gonka1%039d", index)
}

func benchParticipants(count int) []string {
	hosts := make([]string, 0, count)
	for index := range count {
		hosts = append(hosts, benchParticipant(index))
	}
	return hosts
}

func benchLimiter(hosts []string) *ParticipantLimiter {
	limiter := NewParticipantLimiter(
		ParticipantConfig{
			Pricing: WindowPricing{
				Input:                 RequestBounds{Min: 1, Initial: 8},
				Output:                RequestBounds{Min: 1, Initial: 8},
				FallbackContextTokens: 1,
				FallbackOutputTokens:  1,
			},
			Factors: CongestionFactors{Soft: 0.85, Hard: 0.70, Severe: 0.50, Cross: 0.90},
			Slack:   0.30, AfterFailures: 3, BaseOpen: time.Second, MaxOpen: time.Minute,
		},
		func() time.Time { return benchClock },
	)
	for _, participant := range hosts {
		if release, admitted := limiter.Acquire(participant, benchModel, benchCost); admitted == AdmissionOpen {
			release()
		}
	}
	return limiter
}

// BenchmarkParticipantAvailable is the routing pre-filter: one call per host of the group, per drain.
func BenchmarkParticipantAvailable(b *testing.B) {
	hosts := benchParticipants(benchHosts)
	limiter := benchLimiter(hosts)
	b.ReportAllocs()
	for b.Loop() {
		for _, participant := range hosts {
			if !limiter.Available(participant, benchModel) {
				b.Fatal("host should be available")
			}
		}
	}
}

// BenchmarkParticipantAvailableParallel is the shape that matters: many request goroutines asking at once.
func BenchmarkParticipantAvailableParallel(b *testing.B) {
	hosts := benchParticipants(benchHosts)
	limiter := benchLimiter(hosts)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			for _, participant := range hosts {
				limiter.Available(participant, benchModel)
			}
		}
	})
}

// BenchmarkParticipantPicksBesideAttempts is a request as the gateway makes it, from many goroutines at once: one Admits per candidate escrow, then one Acquire and its release.
func BenchmarkParticipantPicksBesideAttempts(b *testing.B) {
	hosts := benchParticipants(benchNetwork)
	for _, candidates := range []int{4, 16, 64} {
		for _, requestsInFlight := range []int{1, 10, 100, 1000} {
			b.Run(fmt.Sprintf("E=%d/N=%d", candidates, requestsInFlight), func(b *testing.B) {
				limiter := benchLimiter(hosts)
				var next atomic.Int64
				total := int64(b.N)
				b.ReportAllocs()
				b.ResetTimer()
				var requests sync.WaitGroup
				for range requestsInFlight {
					requests.Go(func() {
						for request := next.Add(1); request <= total; request = next.Add(1) {
							first := int(request)
							for offset := range candidates {
								limiter.Admits(hosts[(first+offset)%len(hosts)], benchModel)
							}
							if release, admitted := limiter.Acquire(hosts[first%len(hosts)], benchModel, benchCost); admitted == AdmissionOpen {
								release()
							}
						}
					})
				}
				requests.Wait()
			})
		}
	}
}

// BenchmarkParticipantAttempt is one dispatched attempt: the slot, its release, and the verdict.
func BenchmarkParticipantAttempt(b *testing.B) {
	hosts := benchParticipants(benchHosts)
	limiter := benchLimiter(hosts)
	b.ReportAllocs()
	index := 0
	for b.Loop() {
		participant := hosts[index%len(hosts)]
		index++
		if release, admitted := limiter.Acquire(participant, benchModel, benchCost); admitted == AdmissionOpen {
			release()
		}
		limiter.OnResult(benchResult(participant))
	}
}

// BenchmarkParticipantAttemptParallel measures the same attempt under the contention a busy gateway has.
func BenchmarkParticipantAttemptParallel(b *testing.B) {
	hosts := benchParticipants(benchHosts)
	limiter := benchLimiter(hosts)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		index := 0
		for pb.Next() {
			participant := hosts[index%len(hosts)]
			index++
			if release, admitted := limiter.Acquire(participant, benchModel, benchCost); admitted == AdmissionOpen {
				release()
			}
			limiter.OnResult(benchResult(participant))
		}
	})
}

// BenchmarkParticipantSnapshot is the metrics scrape over a whole group.
func BenchmarkParticipantSnapshot(b *testing.B) {
	limiter := benchLimiter(benchParticipants(benchHosts))
	b.ReportAllocs()
	for b.Loop() {
		windowSink = limiter.Snapshot()
	}
}

func benchCapacityModel(hosts []string) *Capacity {
	limiter := benchLimiter(hosts)
	capacity := NewCapacity(limiter.Available)
	network := benchParticipants(benchNetwork)
	current := make(map[string]float64, len(network))
	full := make(map[string]float64, len(network))
	for index, participant := range network {
		current[participant] = float64(1000 + index)
		full[participant] = float64(1200 + index)
	}
	capacity.Update(chain.PhaseSnapshot{
		CurrentWeightsByModel: map[string]map[string]float64{benchModel: current},
		FullWeightsByModel:    map[string]map[string]float64{benchModel: full},
	})
	shares := make(map[string]float64, len(hosts))
	for _, participant := range hosts {
		shares[participant] = 0.5
	}
	capacity.SetEscrowMembership("escrow-bench", shares)
	return capacity
}

// BenchmarkCapacityEscrowWeight is the escrow score: one call per candidate escrow, on every pick.
func BenchmarkCapacityEscrowWeight(b *testing.B) {
	capacity := benchCapacityModel(benchParticipants(benchHosts))
	b.ReportAllocs()
	for b.Loop() {
		floatSink = capacity.EscrowWeight("escrow-bench", benchModel)
	}
}

// BenchmarkCapacityForModel is what admission reads per request: the weights and the scale factor.
func BenchmarkCapacityForModel(b *testing.B) {
	capacity := benchCapacityModel(benchParticipants(benchHosts))
	b.ReportAllocs()
	for b.Loop() {
		weights := capacity.ModelWeights(benchModel, false)
		floatSink = weights.CurrentWeight + weights.BaselineWeight + weights.ScaleFactor
	}
}

// BenchmarkCapacityForModelParallel is the same read from many request goroutines at once.
func BenchmarkCapacityForModelParallel(b *testing.B) {
	capacity := benchCapacityModel(benchParticipants(benchHosts))
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			weights := capacity.ModelWeights(benchModel, false)
			floatSink = weights.CurrentWeight + weights.BaselineWeight + weights.ScaleFactor
		}
	})
}
