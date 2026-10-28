package scheduler

import (
	"fmt"
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/limits"
)

// benchGroupSize stays under ten slots, which benchLimiter's single-digit read needs.
const benchGroupSize = 4

type benchSession struct {
	latestNonce uint64
	slots       []string
}

func (b *benchSession) Advance(func(HostBinding) NonceIntent) (Prepared, error) { return nil, nil }
func (b *benchSession) ParticipantKeys() []string                               { return b.slots }
func (b *benchSession) SlotParticipants() []string                              { return b.slots }
func (b *benchSession) ChatBudgetSpent(string) bool                             { return false }
func (b *benchSession) GroupSize() int                                          { return len(b.slots) }
func (b *benchSession) LatestNonce() uint64                                     { return b.latestNonce }
func (b *benchSession) Balance() uint64                                         { return 1 << 40 }
func (b *benchSession) TokenPrice() uint64                                      { return 1 }
func (b *benchSession) FeePerNonce() uint64                                     { return 0 }

// benchLimiter answers without a lock, so the benchmark holds the walk's own length rather than what one rung of the ladder costs.
type benchLimiter struct{ blockedSlots int }

func (l benchLimiter) Admits(participant, _ string) limits.Admission {
	if int(participant[len(participant)-1]-'0') < l.blockedSlots {
		return limits.AdmissionWindowFull
	}
	return limits.AdmissionOpen
}

func (l benchLimiter) Acquire(string, string, limits.TokenCost) (func(), limits.Admission) {
	return func() {}, limits.AdmissionOpen
}

func (l benchLimiter) Overdraft(string, string, limits.TokenCost) (func(), limits.Admission) {
	return func() {}, limits.AdmissionOpen
}

type benchEscrows struct{ byModel map[string][]Escrow }

func (b *benchEscrows) Candidates(model string) []Escrow { return b.byModel[model] }

func (b *benchEscrows) RetiredAtNonceCap(string) (Escrow, bool) { return Escrow{}, false }

type benchWeights struct{ byEscrow map[string]float64 }

func (b *benchWeights) EscrowWeight(escrowID, model string) float64 { return b.byEscrow[escrowID] }

func benchScheduler(escrows, models int) (*Scheduler, []string) {
	return benchSchedulerBlocking(escrows, models, 0)
}

// benchSchedulerBlocking prices the forecast: blockedSlots is how many slots of every group the walk steps over.
func benchSchedulerBlocking(escrows, models, blockedSlots int) (*Scheduler, []string) {
	source := &benchEscrows{byModel: map[string][]Escrow{}}
	weights := &benchWeights{byEscrow: map[string]float64{}}
	modelNames := make([]string, 0, models)
	for index := range models {
		modelNames = append(modelNames, fmt.Sprintf("model-%d", index))
	}
	for index := range escrows {
		id := fmt.Sprintf("escrow-%d", index)
		model := modelNames[index%models]
		source.byModel[model] = append(source.byModel[model], Escrow{
			ID:          id,
			Model:       model,
			Session:     &benchSession{slots: slotsOf(id, benchGroupSize)},
			ActiveUsers: index % 7,
		})
		weights.byEscrow[id] = 1
	}
	return &Scheduler{
		escrows:     source,
		capacity:    weights,
		limiter:     benchLimiter{blockedSlots: blockedSlots},
		perf:        &leanHealth{},
		dispatchers: map[string]*dispatcher{},
	}, modelNames
}

func BenchmarkPickEscrow(b *testing.B) {
	snapshot := chain.PhaseSnapshot{}
	for _, escrows := range []int{1, 10, 100, 1000} {
		models := min(4, escrows)
		scheduler, modelNames := benchScheduler(escrows, models)
		b.Run(fmt.Sprintf("escrows=%d", escrows), func(b *testing.B) {
			b.ReportAllocs()
			for i := range b.N {
				if _, err := pickWith(scheduler, RequestProfile{Model: modelNames[i%len(modelNames)]}, snapshot); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkPickEscrowParallel(b *testing.B) {
	snapshot := chain.PhaseSnapshot{}
	scheduler, modelNames := benchScheduler(100, 4)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if _, err := pickWith(scheduler, RequestProfile{Model: modelNames[i%len(modelNames)]}, snapshot); err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
}

// BenchmarkPickEscrowDegraded times selection as capacity disappears.
func BenchmarkPickEscrowDegraded(b *testing.B) {
	snapshot := chain.PhaseSnapshot{}
	for _, deadPercent := range []int{0, 50, 90, 100} {
		scheduler, modelNames := benchScheduler(100, 4)
		weights := scheduler.capacity.(*benchWeights)
		dead := len(weights.byEscrow) * deadPercent / 100
		for index := range dead {
			weights.byEscrow[fmt.Sprintf("escrow-%d", index)] = 0
		}
		b.Run(fmt.Sprintf("dead=%d%%", deadPercent), func(b *testing.B) {
			b.ReportAllocs()
			for i := range b.N {
				_, _ = pickWith(scheduler, RequestProfile{Model: modelNames[i%len(modelNames)]}, snapshot)
			}
		})
	}
}

// BenchmarkPickEscrowNonceSweep walks one escrow's whole nonce budget to its governance cap.
func BenchmarkPickEscrowNonceSweep(b *testing.B) {
	snapshot := chain.PhaseSnapshot{MaxNonce: 20_000}
	scheduler, modelNames := benchScheduler(100, 4)
	source := scheduler.escrows.(*benchEscrows)
	tracked := source.byModel[modelNames[0]][0].Session.(*benchSession)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		tracked.latestNonce = uint64(i % 20_000)
		if _, err := pickWith(scheduler, RequestProfile{Model: modelNames[0]}, snapshot); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPickEscrowForecast is what the burn forecast costs the pick, priced by how many slots the walk steps over.
func BenchmarkPickEscrowForecast(b *testing.B) {
	snapshot := chain.PhaseSnapshot{}
	for _, blockedSlots := range []int{0, 2, benchGroupSize - 1} {
		scheduler, modelNames := benchSchedulerBlocking(100, 4, blockedSlots)
		b.Run(fmt.Sprintf("blocked=%d/%d", blockedSlots, benchGroupSize), func(b *testing.B) {
			b.ReportAllocs()
			for i := range b.N {
				if _, err := pickWith(scheduler, RequestProfile{Model: modelNames[i%len(modelNames)]}, snapshot); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkPickEscrowSingleModel isolates per-candidate cost from the model-filter effect.
func BenchmarkPickEscrowSingleModel(b *testing.B) {
	snapshot := chain.PhaseSnapshot{}
	for _, escrows := range []int{10, 100, 1000} {
		scheduler, modelNames := benchScheduler(escrows, 1)
		b.Run(fmt.Sprintf("escrows=%d", escrows), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if _, err := pickWith(scheduler, RequestProfile{Model: modelNames[0]}, snapshot); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
